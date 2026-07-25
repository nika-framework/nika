package microservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// Context keys under which the message metadata is published to handlers.
const (
	contextEnvelopeKey = "nika.microservice.envelope"
	contextRouteKey    = "nika.microservice.route"
)

// internalRoutePrefix is where message handlers are mounted on the internal
// engine. It is not reachable over HTTP — the engine is separate from the app's
// HTTP engine.
const internalRoutePrefix = "/_nika/message/"

// internalHost is the Host header on a synthesized request. Messages have no
// host; a fixed, obviously-internal value keeps any host-based logic from acting
// on something a publisher could influence.
const internalHost = "nika.internal"

// contentTypeJSON is shared rather than rebuilt, so the header map costs one
// slice header per dispatch instead of a fresh string slice.
var contentTypeJSON = []string{"application/json; charset=utf-8"}

// dispatcher turns an Envelope into a handler invocation.
//
// Rather than hand-building a *gin.Context, each handler is mounted on a private
// gin engine at a generated path and messages are dispatched through
// engine.ServeHTTP. That is what makes a message handler behave *identically* to
// an HTTP handler: guards, middleware, c.Next/c.Abort, ShouldBindJSON, c.JSON and
// the validator helpers all work unchanged, because they are running against a
// real gin request. The alternative — a synthetic context — reimplements gin's
// semantics and drifts from them on every gin release.
type dispatcher struct {
	engine *gin.Engine

	// bufPool recycles response buffers; a busy consumer dispatches thousands of
	// messages a second and each would otherwise allocate a fresh buffer.
	bufPool sync.Pool
}

func newDispatcher(middleware []gin.HandlerFunc, recovery bool) *dispatcher {
	engine := gin.New()
	// The internal engine never sees a real network peer, so proxy trust is
	// meaningless here; disable it explicitly so ClientIP cannot be influenced
	// by envelope headers.
	_ = engine.SetTrustedProxies(nil)
	engine.RedirectTrailingSlash = false
	engine.RedirectFixedPath = false
	engine.HandleMethodNotAllowed = false

	// The seed middleware must run first so guards, not just handlers, can read
	// the envelope.
	engine.Use(contextSeedMiddleware())

	// This engine is separate from the app's HTTP engine, so the app's recovery
	// middleware does not reach it. Recovery matters more here than it does for
	// HTTP: an escaping panic kills the process, which stops consuming *every*
	// subject, not just the one that failed.
	if recovery {
		engine.Use(nika.RecoveryMiddleware())
	}

	if len(middleware) > 0 {
		engine.Use(middleware...)
	}

	return &dispatcher{
		engine: engine,
		bufPool: sync.Pool{
			New: func() any { return new(bytes.Buffer) },
		},
	}
}

// mount registers a handler chain for a route and assigns its internal path.
func (d *dispatcher) mount(route *Route, handlers []gin.HandlerFunc) {
	route.path = internalRoutePrefix + routeToken(route)

	// Pre-parse the path once. Every dispatch needs a *url.URL, and parsing the
	// same static string per message is pure waste on a consumer's hot path — a
	// struct copy from this template costs a fraction of a parse.
	route.url = &url.URL{Path: route.path}

	d.engine.POST(route.path, handlers...)
}

// routeToken derives a collision-free, URL-safe path segment for a route.
// Deriving it from a counter rather than from the pattern means no pattern
// character (`*`, `/`, `%`, `..`) can ever influence path parsing.
func routeToken(route *Route) string {
	routeCounterMu.Lock()
	routeCounter++
	n := routeCounter
	routeCounterMu.Unlock()
	return fmt.Sprintf("%s-%d", sanitizeToken(route.Transport), n)
}

var (
	routeCounterMu sync.Mutex
	routeCounter   int
)

// sanitizeToken keeps only characters that are unambiguous in a URL path.
func sanitizeToken(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "t"
	}
	return b.String()
}

// dispatchRoute runs an already-resolved route's handler chain against the
// envelope and converts the result back into a reply envelope.
func (d *dispatcher) dispatchRoute(ctx context.Context, env *Envelope, route *Route) (*Envelope, error) {
	if env == nil {
		return nil, fmt.Errorf("microservice: nil envelope")
	}
	if route == nil || route.path == "" {
		return replyError(env, http.StatusNotFound, "PATTERN_NOT_FOUND",
			fmt.Sprintf("no handler is mounted for pattern %q", env.Pattern)), ErrNoHandler
	}

	body := env.Data
	if len(body) == 0 {
		// An empty payload must still be valid JSON so ShouldBindJSON reports a
		// validation error rather than a parse error.
		body = json.RawMessage("null")
	}

	// The URL is copied by value from the route's template rather than shared:
	// a handler or middleware that mutates req.URL would otherwise corrupt every
	// concurrent dispatch on the same route.
	requestURL := *route.url

	req := (&http.Request{
		Method:     http.MethodPost,
		URL:        &requestURL,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type": contentTypeJSON,
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Host:          internalHost,
		// The peer is the broker, not an IP client. A fixed loopback address keeps
		// ClientIP()-based logic from reading meaningless values.
		RemoteAddr: "127.0.0.1:0",
	}).WithContext(ctx)

	applyEnvelopeHeaders(req, env)

	buf := d.bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer d.bufPool.Put(buf)

	recorder := &responseCapture{header: make(http.Header, 8), body: buf}

	// Publish the envelope so handlers can read the pattern, id and headers.
	// gin has no hook to seed a context before the chain runs, so a tiny
	// middleware installed once per engine does it from the request context.
	ctxWithEnvelope := context.WithValue(req.Context(), envelopeCtxKey{}, envelopeCtx{env: env, route: route})
	d.engine.ServeHTTP(recorder, req.WithContext(ctxWithEnvelope))

	return d.buildReply(env, recorder), nil
}

// buildReply converts the captured HTTP response into a reply envelope.
func (d *dispatcher) buildReply(env *Envelope, recorder *responseCapture) *Envelope {
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}

	// Copy the body out of the pooled buffer: the buffer goes back to the pool
	// when Dispatch returns, and the reply outlives that.
	payload := make([]byte, recorder.body.Len())
	copy(payload, recorder.body.Bytes())

	reply := &Envelope{
		ID:      env.ID,
		Pattern: env.Pattern,
		Status:  status,
	}
	if len(payload) > 0 && json.Valid(payload) {
		reply.Data = payload
	} else if len(payload) > 0 {
		// A non-JSON body (a plain string, a rendered template) is wrapped so the
		// envelope stays valid JSON end to end.
		encoded, err := json.Marshal(string(payload))
		if err == nil {
			reply.Data = encoded
		}
	}

	if status >= http.StatusBadRequest {
		reply.Error = extractError(status, payload)
	}
	return reply
}

// extractError reuses the framework's error body when the handler produced one,
// so a microservice caller sees the same error shape as an HTTP caller.
func extractError(status int, payload []byte) *EnvelopeError {
	var framed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Details any    `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &framed) == nil && framed.Error != nil {
		code := framed.Error.Code
		if code == 0 {
			code = status
		}
		return &EnvelopeError{
			Code:    code,
			Message: framed.Error.Message,
			Details: framed.Error.Details,
		}
	}

	return &EnvelopeError{Code: status, Message: http.StatusText(status)}
}

// replyError builds a failed reply for a message that never reached a handler.
func replyError(env *Envelope, status int, code, message string) *Envelope {
	reply := &Envelope{Pattern: env.Pattern, Status: status,
		Error: &EnvelopeError{Code: status, Message: code, Details: message}}
	if env != nil {
		reply.ID = env.ID
	}
	return reply
}

// hopByHopHeaders are meaningless on a synthesized request and would confuse
// binding or the recorder if a publisher set them.
var hopByHopHeaders = map[string]struct{}{
	"content-length":      {},
	"transfer-encoding":   {},
	"connection":          {},
	"keep-alive":          {},
	"host":                {},
	"upgrade":             {},
	"content-type":        {},
	"expect":              {},
	"te":                  {},
	"trailer":             {},
	"proxy-authorization": {},
}

// applyEnvelopeHeaders copies envelope headers onto the request.
//
// Values come from a remote publisher, so anything that could smuggle a second
// header (CR/LF), spoof the client IP (X-Forwarded-For), or override the body
// framing is dropped rather than sanitised — silently rewriting an attacker's
// header is harder to reason about than refusing it.
func applyEnvelopeHeaders(req *http.Request, env *Envelope) {
	for key, value := range env.Headers {
		if key == "" || strings.ContainsAny(key, "\r\n\x00:") {
			continue
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			continue
		}
		lower := strings.ToLower(key)
		if _, blocked := hopByHopHeaders[lower]; blocked {
			continue
		}
		if lower == "x-forwarded-for" || lower == "x-real-ip" {
			continue
		}
		req.Header.Set(key, value)
	}
}

// responseCapture is the minimal http.ResponseWriter the internal engine writes
// into. gin wraps it in its own ResponseWriter, so only the three standard
// methods are needed.
type responseCapture struct {
	header      http.Header
	body        *bytes.Buffer
	status      int
	wroteHeader bool
}

func (r *responseCapture) Header() http.Header { return r.header }

func (r *responseCapture) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(p)
}

func (r *responseCapture) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
}

// envelopeCtxKey and envelopeCtx carry the message metadata through the request
// context so the seeding middleware can lift it into the gin context.
type envelopeCtxKey struct{}

type envelopeCtx struct {
	env   *Envelope
	route *Route
}

// contextSeedMiddleware moves the envelope from the request context onto the gin
// context. It is installed as the engine's first middleware so every handler and
// guard can read it.
func contextSeedMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if carried, ok := c.Request.Context().Value(envelopeCtxKey{}).(envelopeCtx); ok {
			c.Set(contextEnvelopeKey, carried.env)
			c.Set(contextRouteKey, carried.route)
		}
		c.Next()
	}
}

// MessageFrom returns the envelope being handled, or nil for an HTTP request.
// It is what lets one handler serve both an HTTP route and a message subject.
func MessageFrom(c *gin.Context) *Envelope {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(contextEnvelopeKey); ok {
		if env, ok := v.(*Envelope); ok {
			return env
		}
	}
	return nil
}

// PatternFrom returns the pattern the current message was addressed to. For a
// wildcard handler this is the *literal* subject the client sent — so a handler
// registered as "user_*" can read "user_23".
func PatternFrom(c *gin.Context) string {
	if env := MessageFrom(c); env != nil {
		return env.Pattern
	}
	return ""
}

// RouteFrom returns the route that matched, including its declared pattern.
func RouteFrom(c *gin.Context) *Route {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(contextRouteKey); ok {
		if route, ok := v.(*Route); ok {
			return route
		}
	}
	return nil
}

// IsMessage reports whether the handler is serving a message rather than HTTP.
func IsMessage(c *gin.Context) bool {
	return MessageFrom(c) != nil
}
