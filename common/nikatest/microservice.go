package nikatest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/nika-framework/nika"
	"github.com/nika-framework/nika/common/microservice"
)

// Microservice is the harness for message handlers — the fields tagged
// `transport:"..." pattern:"..."` on a controller.
//
// It wires the in-memory transport, so a test exercises the real dispatch path
// (router, guards, middleware, binding, validation, response encoding) with no
// broker running:
//
//	ms := nikatest.NewMicroservice(t)
//	ms.LoadModule(src.NewAppModule())
//
//	ms.Send("user_created", CreateUserDto{Name: "Ada"}).
//	    ExpectStatus(201).
//	    ExpectJSONPath("data.name", "Ada")
type Microservice struct {
	app       *App
	transport *microservice.MemoryTransport
	server    *microservice.Server
	timeout   time.Duration
}

// NewMicroservice boots an app with the in-memory transport attached.
func NewMicroservice(t TB, opts ...Options) *Microservice {
	t.Helper()
	return Attach(New(t, opts...))
}

// Attach adds the in-memory transport to an existing app under test, so one test
// can drive the same controllers over both HTTP and messages.
func Attach(app *App) *Microservice {
	app.t.Helper()

	transport := microservice.NewMemory()
	server, err := microservice.Setup(app.Nika(), microservice.Config{
		Transport: transport,
		// A short handler timeout keeps a deadlocked handler from stalling the
		// suite; the test's own deadline is the real bound.
		HandlerTimeout: app.timeout,
	})
	if err != nil {
		app.t.Fatalf("nikatest: attaching the memory transport failed: %v", err)
		return nil
	}

	ms := &Microservice{
		app:       app,
		transport: transport,
		server:    server,
		timeout:   app.timeout,
	}

	app.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})

	return ms
}

// App returns the underlying app under test, for HTTP assertions on the same
// controllers.
func (m *Microservice) App() *App { return m.app }

// Server returns the microservice server.
func (m *Microservice) Server() *microservice.Server { return m.server }

// Transport returns the in-memory transport, for a test that wants to publish
// through the queue rather than dispatch directly.
func (m *Microservice) Transport() *microservice.MemoryTransport { return m.transport }

// LoadModule loads a module graph and starts the consumers.
func (m *Microservice) LoadModule(module nika.Module) *Microservice {
	m.app.t.Helper()
	m.app.LoadModule(module)
	m.app.Start()
	return m
}

// RegisterControllers registers controllers directly and starts the consumers.
func (m *Microservice) RegisterControllers(controllers ...any) *Microservice {
	m.app.t.Helper()
	m.app.RegisterControllers(controllers...)
	m.app.Start()
	return m
}

// Transport name used by the harness, for tags in test fixtures. A controller
// under test declares `transport:"memory"`.
const TransportName = microservice.TransportMemory

// Patterns returns every registered message pattern, for asserting the surface a
// service exposes.
func (m *Microservice) Patterns() []string {
	if router := m.server.Router(microservice.TransportMemory); router != nil {
		return router.Patterns()
	}
	return nil
}

// ExpectPattern fails the test when no handler is registered for a pattern.
func (m *Microservice) ExpectPattern(pattern string) *Microservice {
	m.app.t.Helper()

	for _, registered := range m.Patterns() {
		if registered == pattern {
			return m
		}
	}
	m.app.t.Errorf("nikatest: pattern %q is not registered.\n  registered: %v", pattern, m.Patterns())
	return m
}

// ExpectRoutesTo asserts that a literal subject dispatches to the handler
// declared for the given pattern.
//
// This is the assertion that pins wildcard precedence: with handlers on both
// "user_created" and "user_*", it proves "user_created" reaches the exact
// handler and "user_23" reaches the wildcard one — the behaviour most likely to
// regress when patterns are added.
func (m *Microservice) ExpectRoutesTo(subject, wantPattern string) *Microservice {
	m.app.t.Helper()

	router := m.server.Router(microservice.TransportMemory)
	if router == nil {
		m.app.t.Errorf("nikatest: the memory transport has no router")
		return m
	}

	route, found := router.Resolve(subject)
	if !found {
		m.app.t.Errorf("nikatest: subject %q does not match any pattern.\n  registered: %v",
			subject, router.Patterns())
		return m
	}
	if string(route.Pattern) != wantPattern {
		m.app.t.Errorf("nikatest: subject %q resolved to pattern %q, expected %q (handler %s.%s)",
			subject, route.Pattern, wantPattern, route.Controller, route.Field)
	}
	return m
}

// Send dispatches a message and returns its reply for assertions. It bypasses
// the queue so the result is available synchronously with no polling and no
// sleep — a test that waits on a channel is a test that flakes.
func (m *Microservice) Send(pattern string, payload any) *Reply {
	m.app.t.Helper()

	env, err := microservice.NewEnvelope(pattern, payload)
	if err != nil {
		m.app.t.Fatalf("nikatest: building the %q envelope failed: %v", pattern, err)
		return nil
	}
	return m.dispatch(env)
}

// SendWithHeaders dispatches a message carrying headers, for testing a guard
// that reads an Authorization header off the envelope.
func (m *Microservice) SendWithHeaders(pattern string, payload any, headers map[string]string) *Reply {
	m.app.t.Helper()

	env, err := microservice.NewEnvelope(pattern, payload)
	if err != nil {
		m.app.t.Fatalf("nikatest: building the %q envelope failed: %v", pattern, err)
		return nil
	}
	for key, value := range headers {
		env.WithHeader(key, value)
	}
	return m.dispatch(env)
}

// SendRaw dispatches a raw JSON payload, for exercising the malformed-input path.
func (m *Microservice) SendRaw(pattern string, rawJSON string) *Reply {
	m.app.t.Helper()

	env, err := microservice.NewEnvelope(pattern, json.RawMessage(rawJSON))
	if err != nil {
		m.app.t.Fatalf("nikatest: building the %q envelope failed: %v", pattern, err)
		return nil
	}
	return m.dispatch(env)
}

// Emit publishes through the queue and returns once the transport accepts it,
// for testing a fire-and-forget flow. Prefer Send: a test that asserts on a
// side effect after Emit has to wait for it, and Send removes that race.
func (m *Microservice) Emit(pattern string, payload any) *Microservice {
	m.app.t.Helper()

	env, err := microservice.NewEnvelope(pattern, payload)
	if err != nil {
		m.app.t.Fatalf("nikatest: building the %q envelope failed: %v", pattern, err)
		return m
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	if err := m.transport.Publish(ctx, env); err != nil {
		m.app.t.Fatalf("nikatest: publishing %q failed: %v", pattern, err)
	}
	return m
}

// Client returns a real microservice client over the in-memory transport, for
// testing code that itself calls out through a *microservice.Client.
func (m *Microservice) Client() *microservice.Client {
	return microservice.NewClient(m.transport, microservice.WithTimeout(m.timeout))
}

func (m *Microservice) dispatch(env *microservice.Envelope) *Reply {
	m.app.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	reply, err := m.transport.Dispatch(ctx, env)
	if err != nil {
		m.app.t.Fatalf("nikatest: dispatching %q failed: %v", env.Pattern, err)
		return nil
	}

	return &Reply{
		Response: &Response{
			t:        m.app.t,
			method:   "MSG",
			path:     env.Pattern,
			raw:      reply.Data,
			recorder: statusRecorder(reply.Status),
		},
		envelope: reply,
	}
}

// statusRecorder adapts a reply status onto the recorder the Response
// assertions read, so message replies and HTTP responses share one assertion
// vocabulary instead of two parallel ones that drift apart.
func statusRecorder(status int) *httptest.ResponseRecorder {
	if status == 0 {
		status = http.StatusOK
	}
	recorder := httptest.NewRecorder()
	recorder.Code = status
	recorder.Header().Set("Content-Type", "application/json; charset=utf-8")
	return recorder
}

// Reply is a message reply, with every Response assertion available plus the
// envelope-level ones.
type Reply struct {
	*Response
	envelope *microservice.Envelope
}

// Envelope returns the raw reply envelope.
func (r *Reply) Envelope() *microservice.Envelope { return r.envelope }

// ExpectPattern asserts the reply echoes the pattern it answered.
func (r *Reply) ExpectPattern(want string) *Reply {
	r.t.Helper()
	if r.envelope.Pattern != want {
		r.t.Errorf("message %s: expected reply pattern %q, got %q",
			r.path, want, r.envelope.Pattern)
	}
	return r
}

// ExpectNoError asserts the reply carries no error.
func (r *Reply) ExpectNoError() *Reply {
	r.t.Helper()
	if r.envelope.Error != nil {
		r.t.Errorf("message %s: expected no error, got %s (code %d, details %v)",
			r.path, r.envelope.Error.Message, r.envelope.Error.Code, r.envelope.Error.Details)
	}
	return r
}

// ExpectError asserts the reply carries an error with the given code.
func (r *Reply) ExpectError(wantMessage string) *Reply {
	r.t.Helper()
	if r.envelope.Error == nil {
		r.t.Errorf("message %s: expected error %q, got a successful reply: %s",
			r.path, wantMessage, r.BodyString())
		return r
	}
	if r.envelope.Error.Message != wantMessage {
		r.t.Errorf("message %s: expected error %q, got %q",
			r.path, wantMessage, r.envelope.Error.Message)
	}
	return r
}

// ExpectNoHandler asserts the subject matched no registered pattern.
func (r *Reply) ExpectNoHandler() *Reply {
	r.t.Helper()
	r.Response.ExpectStatus(http.StatusNotFound)
	return r.ExpectError("PATTERN_NOT_FOUND")
}

// The assertions below re-declare the embedded Response ones so they return
// *Reply.
//
// Promoted methods would return *Response, which silently drops the
// envelope-level assertions from the middle of a chain — `Send(...).ExpectOK().
// ExpectNoError()` would not compile. Re-declaring them keeps one fluent chain
// across both levels.

// ExpectStatus asserts the reply status.
func (r *Reply) ExpectStatus(want int) *Reply {
	r.t.Helper()
	r.Response.ExpectStatus(want)
	return r
}

// ExpectOK asserts a 200 reply.
func (r *Reply) ExpectOK() *Reply { r.t.Helper(); r.Response.ExpectOK(); return r }

// ExpectCreated asserts a 201 reply.
func (r *Reply) ExpectCreated() *Reply { r.t.Helper(); r.Response.ExpectCreated(); return r }

// ExpectSuccess asserts a 2xx reply.
func (r *Reply) ExpectSuccess() *Reply { r.t.Helper(); r.Response.ExpectSuccess(); return r }

// ExpectBadRequest asserts a 400 reply.
func (r *Reply) ExpectBadRequest() *Reply { r.t.Helper(); r.Response.ExpectBadRequest(); return r }

// ExpectUnauthorized asserts a 401 reply.
func (r *Reply) ExpectUnauthorized() *Reply {
	r.t.Helper()
	r.Response.ExpectUnauthorized()
	return r
}

// ExpectForbidden asserts a 403 reply.
func (r *Reply) ExpectForbidden() *Reply { r.t.Helper(); r.Response.ExpectForbidden(); return r }

// ExpectNotFound asserts a 404 reply.
func (r *Reply) ExpectNotFound() *Reply { r.t.Helper(); r.Response.ExpectNotFound(); return r }

// ExpectUnprocessable asserts a 422 reply.
func (r *Reply) ExpectUnprocessable() *Reply {
	r.t.Helper()
	r.Response.ExpectUnprocessable()
	return r
}

// ExpectJSON asserts the reply payload contains the given structure.
func (r *Reply) ExpectJSON(want any) *Reply { r.t.Helper(); r.Response.ExpectJSON(want); return r }

// ExpectJSONEquals asserts the reply payload equals the given structure.
func (r *Reply) ExpectJSONEquals(want any) *Reply {
	r.t.Helper()
	r.Response.ExpectJSONEquals(want)
	return r
}

// ExpectJSONPath asserts the value at a dotted path in the reply payload.
func (r *Reply) ExpectJSONPath(path string, want any) *Reply {
	r.t.Helper()
	r.Response.ExpectJSONPath(path, want)
	return r
}

// ExpectJSONPathExists asserts the paths are present in the reply payload.
func (r *Reply) ExpectJSONPathExists(paths ...string) *Reply {
	r.t.Helper()
	r.Response.ExpectJSONPathExists(paths...)
	return r
}

// ExpectJSONPathAbsent asserts the paths are absent from the reply payload.
func (r *Reply) ExpectJSONPathAbsent(paths ...string) *Reply {
	r.t.Helper()
	r.Response.ExpectJSONPathAbsent(paths...)
	return r
}

// ExpectJSONLen asserts the length at a path in the reply payload.
func (r *Reply) ExpectJSONLen(path string, want int) *Reply {
	r.t.Helper()
	r.Response.ExpectJSONLen(path, want)
	return r
}

// ExpectContains asserts the reply payload contains every substring.
func (r *Reply) ExpectContains(wants ...string) *Reply {
	r.t.Helper()
	r.Response.ExpectContains(wants...)
	return r
}

// ExpectNotContains asserts the reply payload contains none of the substrings.
func (r *Reply) ExpectNotContains(unwanted ...string) *Reply {
	r.t.Helper()
	r.Response.ExpectNotContains(unwanted...)
	return r
}

// ExpectAPISuccess asserts the framework's success envelope.
func (r *Reply) ExpectAPISuccess() *Reply { r.t.Helper(); r.Response.ExpectAPISuccess(); return r }

// ExpectAPIError asserts the framework's error envelope with the given code.
func (r *Reply) ExpectAPIError(code string) *Reply {
	r.t.Helper()
	r.Response.ExpectAPIError(code)
	return r
}

// ExpectValidationError asserts a 422 reply naming every given field.
func (r *Reply) ExpectValidationError(fields ...string) *Reply {
	r.t.Helper()
	r.Response.ExpectValidationError(fields...)
	return r
}

// DecodeJSON unmarshals the reply payload into out.
func (r *Reply) DecodeJSON(out any) *Reply { r.t.Helper(); r.Response.DecodeJSON(out); return r }

// ExpectGolden compares the reply payload against a stored snapshot.
func (r *Reply) ExpectGolden(name string) *Reply {
	r.t.Helper()
	r.Response.ExpectGolden(name)
	return r
}

// Debug logs the full reply.
func (r *Reply) Debug() *Reply { r.t.Helper(); r.Response.Debug(); return r }
