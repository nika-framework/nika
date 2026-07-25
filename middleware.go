package nika

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader is the header read and written by RequestIDMiddleware.
const RequestIDHeader = "X-Request-ID"

// requestIDContextKey is the gin context key holding the resolved request id.
const requestIDContextKey = "nika.request_id"

// maxInboundRequestIDLen bounds how much of a client-supplied request id we are
// willing to echo. Unbounded ids would let a client bloat every log line and
// response header for their requests.
const maxInboundRequestIDLen = 64

// RecoveryMiddleware converts a panic in any downstream handler into a 500
// response instead of letting it unwind into net/http and kill the connection.
//
// The panic value and stack are logged server-side and never sent to the client:
// stacks disclose file paths, dependency versions and sometimes request data.
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// A closed connection is not a server fault: there is nobody left to
			// answer, so just abandon the request quietly.
			if isBrokenPipe(recovered) {
				logger().Warn("request aborted: client closed connection",
					slog.String("path", c.Request.URL.Path),
					slog.String("request_id", RequestIDFrom(c)),
				)
				c.Abort()
				return
			}

			logger().Error("panic recovered",
				slog.Any("panic", recovered),
				slog.String("method", c.Request.Method),
				slog.String("path", c.Request.URL.Path),
				slog.String("client_ip", c.ClientIP()),
				slog.String("request_id", RequestIDFrom(c)),
				slog.String("stack", string(debug.Stack())),
			)

			if c.Writer.Written() {
				// Headers already flushed — we cannot replace the body with an
				// error document, so just stop the chain.
				c.Abort()
				return
			}

			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error": gin.H{
					"code":    http.StatusInternalServerError,
					"message": "INTERNAL_SERVER_ERROR",
				},
			})
		}()

		c.Next()
	}
}

// isBrokenPipe reports whether a recovered value is a write to a connection the
// peer already closed.
func isBrokenPipe(recovered any) bool {
	err, ok := recovered.(error)
	if !ok {
		return false
	}

	var netErr *net.OpError
	if !errors.As(err, &netErr) {
		return false
	}

	msg := strings.ToLower(netErr.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

// BodyLimitMiddleware caps the number of bytes any handler can read from the
// request body. Without it a single client can stream an unbounded body and
// drive the process out of memory — a body limit enforced inside a handler is
// too late, because binding has already buffered the payload.
//
// The cap is applied with http.MaxBytesReader, so exceeding it surfaces as a
// read error during binding rather than a silent truncation.
func BodyLimitMiddleware(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}

	return func(c *gin.Context) {
		// Reject early when the client declares an oversized body: no reason to
		// read a single byte of it.
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error": gin.H{
					"code":    http.StatusRequestEntityTooLarge,
					"message": "PAYLOAD_TOO_LARGE",
				},
			})
			return
		}

		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// RequestIDMiddleware attaches a request id to every request, reusing an
// inbound X-Request-ID when it is well-formed and generating one otherwise.
//
// Inbound values are validated rather than trusted: an attacker-controlled id
// flows into logs and response headers, so newlines (log forging, header
// splitting) and unbounded lengths must not survive.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitizeRequestID(c.GetHeader(RequestIDHeader))
		if id == "" {
			id = newRequestID()
		}

		c.Set(requestIDContextKey, id)
		c.Writer.Header().Set(RequestIDHeader, id)
		c.Next()
	}
}

// RequestIDFrom returns the request id attached by RequestIDMiddleware, or an
// empty string when the middleware is not installed.
func RequestIDFrom(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, ok := c.Get(requestIDContextKey); ok {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}

// sanitizeRequestID returns the id when it is safe to echo, or "" to force a
// freshly generated one.
func sanitizeRequestID(id string) string {
	if id == "" || len(id) > maxInboundRequestIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	return id
}

// newRequestID returns a 128-bit random hex id.
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand only fails catastrophically; fall back to a timestamp so
		// the request still gets a usable correlation key.
		return "ts-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}

// SecurityHeadersMiddleware sets response headers that cost nothing and close
// off whole classes of browser-side attacks.
//
// It deliberately does not set a Content-Security-Policy: a useful CSP is
// application-specific, and a wrong one silently breaks pages.
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("X-XSS-Protection", "0") // the legacy auditor causes bugs; CSP replaces it

		// Only advertise HSTS on connections that already are TLS: sending it
		// over plain HTTP is ignored by browsers and misleads operators.
		if c.Request.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

// LoggerMiddleware writes one structured line per request.
func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		status := c.Writer.Status()
		attrs := []slog.Attr{
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", c.Writer.Size()),
		}
		if query != "" {
			attrs = append(attrs, slog.String("query", query))
		}
		if id := RequestIDFrom(c); id != "" {
			attrs = append(attrs, slog.String("request_id", id))
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}

		level := slog.LevelInfo
		switch {
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case status >= http.StatusBadRequest:
			level = slog.LevelWarn
		}

		logger().LogAttrs(c.Request.Context(), level, "request", attrs...)
	}
}

// defaultLogger is used until SetLogger installs another one.
var defaultLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

// SetLogger replaces the logger used by the framework's middleware. Passing nil
// restores the default stderr logger.
func SetLogger(l *slog.Logger) {
	loggerMu.Lock()
	activeLogger = l
	loggerMu.Unlock()
}

var (
	loggerMu     sync.RWMutex
	activeLogger *slog.Logger
)

func logger() *slog.Logger {
	loggerMu.RLock()
	l := activeLogger
	loggerMu.RUnlock()
	if l != nil {
		return l
	}
	return defaultLogger
}
