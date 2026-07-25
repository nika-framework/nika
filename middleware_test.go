package nika

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// silenceLogs installs a discarding logger for the duration of a test, so a
// deliberate panic does not print a stack trace into the test output.
func silenceLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { SetLogger(nil) })
	return &buf
}

// TestRecoveryKeepsTheServerAlive is the availability guarantee: gin.New() ships
// no recovery middleware, so before this a single nil dereference in one handler
// unwound into net/http and killed the connection — and with no recovery at all,
// a panic in a goroutine-free handler still returned an empty 500 with no log.
func TestRecoveryTurnsAPanicIntoA500(t *testing.T) {
	logs := silenceLogs(t)

	type controller struct {
		Boom func(*gin.Context) `route:"GET:/boom"`
		Fine func(*gin.Context) `route:"GET:/fine"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{
		Boom: func(c *gin.Context) {
			var repo *repository
			_ = repo.name // deliberate nil dereference
		},
		Fine: okHandler("still alive"),
	})

	res := do(app, "GET", "/boom")
	if res.Code != http.StatusInternalServerError {
		t.Errorf("GET /boom = %d, want 500", res.Code)
	}

	// The stack must stay server-side: it discloses file paths, dependency
	// versions and sometimes request data.
	body := res.Body.String()
	for _, leak := range []string{"goroutine", ".go:", "runtime.", "nil pointer"} {
		if strings.Contains(body, leak) {
			t.Errorf("the 500 body leaked internals (%q): %s", leak, body)
		}
	}
	if !strings.Contains(body, "INTERNAL_SERVER_ERROR") {
		t.Errorf("the 500 body = %s, want the framework error envelope", body)
	}
	if !strings.Contains(logs.String(), "panic recovered") {
		t.Error("the panic was not logged server-side")
	}

	// The process must still be serving.
	if res := do(app, "GET", "/fine"); res.Code != http.StatusOK {
		t.Errorf("GET /fine after a panic = %d, want 200", res.Code)
	}
}

func TestRecoveryCanBeDisabled(t *testing.T) {
	type controller struct {
		Boom func(*gin.Context) `route:"GET:/boom"`
	}

	app := newTestApp(Config{DisableRecovery: true})
	app.RegisterControllers(&controller{
		Boom: func(c *gin.Context) { panic("expected") },
	})

	defer expectPanic(t, "expected")
	do(app, "GET", "/boom")
}

func TestBodyLimitRejectsAnOversizedDeclaredBody(t *testing.T) {
	type controller struct {
		Create func(*gin.Context) `route:"POST:/upload"`
	}

	app := newTestApp(Config{MaxBodyBytes: 32})
	app.RegisterControllers(&controller{
		Create: func(c *gin.Context) {
			// Reaching the handler at all means the limit did not apply.
			c.String(http.StatusOK, "accepted")
		},
	})

	res := do(app, "POST", "/upload", strings.Repeat("a", 128))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /upload with an oversized body = %d, want 413\n  body: %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "accepted") {
		t.Error("the handler ran for an oversized body")
	}
}

func TestBodyLimitTruncatesAnUndeclaredBody(t *testing.T) {
	// A chunked request declares no Content-Length, so the early rejection cannot
	// fire and MaxBytesReader has to do the work during binding. Without it, a
	// handler that reads the body would buffer without bound.
	type controller struct {
		Create func(*gin.Context) `route:"POST:/upload"`
	}

	var readErr error
	app := newTestApp(Config{MaxBodyBytes: 32})
	app.RegisterControllers(&controller{
		Create: func(c *gin.Context) {
			buf := make([]byte, 1024)
			_, readErr = c.Request.Body.Read(buf)
			for readErr == nil {
				_, readErr = c.Request.Body.Read(buf)
			}
			c.Status(http.StatusOK)
		},
	})

	req := httptest.NewRequest("POST", "/upload", strings.NewReader(strings.Repeat("a", 4096)))
	req.ContentLength = -1 // as an unknown-length / chunked body arrives
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("reading an oversized body returned no error, so the cap was not enforced")
	}
	if !strings.Contains(readErr.Error(), "too large") {
		t.Errorf("read error = %v, want a body-too-large error", readErr)
	}
}

func TestBodyLimitAllowsAnAcceptableBody(t *testing.T) {
	type controller struct {
		Create func(*gin.Context) `route:"POST:/things"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{
		Create: func(c *gin.Context) {
			var payload map[string]string
			if err := c.ShouldBindJSON(&payload); err != nil {
				c.String(http.StatusBadRequest, err.Error())
				return
			}
			c.JSON(http.StatusCreated, payload)
		},
	})

	res := do(app, "POST", "/things", `{"name":"Ada"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("POST /things = %d, want 201\n  body: %s", res.Code, res.Body.String())
	}

	var decoded map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("the response is not JSON: %v", err)
	}
	if decoded["name"] != "Ada" {
		t.Errorf("body = %v, want name=Ada", decoded)
	}
}

func TestSanitizeRequestID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain hex passes", in: "9f86d081884c7d65", want: "9f86d081884c7d65"},
		{name: "dashes and underscores pass", in: "req_1-2.3", want: "req_1-2.3"},
		{name: "empty is rejected", in: "", want: ""},
		// A newline in a request id ends up in a log line and a response header:
		// log forging and header splitting from one untrusted field.
		{name: "newline is rejected", in: "abc\ndef", want: ""},
		{name: "carriage return is rejected", in: "abc\rdef", want: ""},
		{name: "header injection attempt is rejected", in: "x\r\nSet-Cookie: a=b", want: ""},
		{name: "null byte is rejected", in: "abc\x00", want: ""},
		{name: "space is rejected", in: "abc def", want: ""},
		{name: "over-long is rejected", in: strings.Repeat("a", 65), want: ""},
		{name: "at the length limit passes", in: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeRequestID(test.in); got != test.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	type controller struct {
		Ping func(*gin.Context) `route:"GET:/ping"`
	}

	newApp := func() (*App, *string) {
		seen := new(string)
		app := newTestApp(Config{RequestID: true})
		app.RegisterControllers(&controller{
			Ping: func(c *gin.Context) {
				*seen = RequestIDFrom(c)
				c.Status(http.StatusOK)
			},
		})
		return app, seen
	}

	t.Run("generates one when absent", func(t *testing.T) {
		app, seen := newApp()
		res := do(app, "GET", "/ping")

		if *seen == "" {
			t.Error("no request id reached the handler")
		}
		if got := res.Header().Get(RequestIDHeader); got != *seen {
			t.Errorf("response %s = %q, want the handler's id %q", RequestIDHeader, got, *seen)
		}
		if len(*seen) != 32 {
			t.Errorf("generated id %q is %d chars, want 32 hex chars", *seen, len(*seen))
		}
	})

	t.Run("reuses a well-formed inbound id", func(t *testing.T) {
		app, seen := newApp()
		req := httptest.NewRequest("GET", "/ping", nil)
		req.Header.Set(RequestIDHeader, "trace-abc-123")
		app.Handler().ServeHTTP(httptest.NewRecorder(), req)

		if *seen != "trace-abc-123" {
			t.Errorf("request id = %q, want the inbound \"trace-abc-123\"", *seen)
		}
	})

	t.Run("replaces a hostile inbound id", func(t *testing.T) {
		app, seen := newApp()
		req := httptest.NewRequest("GET", "/ping", nil)
		// http.Header.Set would itself reject a raw newline, so use the escaped
		// form a proxy might pass through.
		req.Header["X-Request-Id"] = []string{"evil id with spaces"}

		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, req)

		if *seen == "evil id with spaces" {
			t.Error("an unvalidated request id was echoed into logs and headers")
		}
		if *seen == "" {
			t.Error("no replacement id was generated")
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	type controller struct {
		Page func(*gin.Context) `route:"GET:/page"`
	}

	app := newTestApp(Config{SecurityHeaders: true})
	app.RegisterControllers(&controller{Page: okHandler("<h1>hi</h1>")})

	res := do(app, "GET", "/page")

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "strict-origin-when-cross-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for header, wantValue := range want {
		if got := res.Header().Get(header); got != wantValue {
			t.Errorf("%s = %q, want %q", header, got, wantValue)
		}
	}

	// HSTS over plain HTTP is ignored by browsers and misleads whoever reads the
	// response, so it must only appear on a TLS connection.
	if got := res.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q on a plaintext request, want it unset", got)
	}
}

// TestClientIPIsNotSpoofable is the highest-impact default in the framework: gin
// trusts every proxy out of the box, so any client could set X-Forwarded-For and
// defeat IP-based rate limiting and allow-lists.
func TestClientIPIsNotSpoofable(t *testing.T) {
	type controller struct {
		Who func(*gin.Context) `route:"GET:/who"`
	}

	register := func(app *App) {
		app.RegisterControllers(&controller{
			Who: func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) },
		})
	}

	t.Run("forged header is ignored by default", func(t *testing.T) {
		app := newTestApp()
		register(app)

		req := httptest.NewRequest("GET", "/who", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")

		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, req)

		if got := recorder.Body.String(); got != "203.0.113.9" {
			t.Errorf("ClientIP() = %q, want the socket peer 203.0.113.9 — a forged X-Forwarded-For was trusted", got)
		}
	})

	t.Run("header is honoured for a configured proxy", func(t *testing.T) {
		app := newTestApp(Config{TrustedProxies: []string{"203.0.113.0/24"}})
		register(app)

		req := httptest.NewRequest("GET", "/who", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")

		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, req)

		if got := recorder.Body.String(); got != "1.2.3.4" {
			t.Errorf("ClientIP() = %q, want 1.2.3.4 from the trusted proxy", got)
		}
	})
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}.withDefaults()

	if cfg.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, DefaultMaxBodyBytes)
	}
	if cfg.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", cfg.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	// ReadTimeout and WriteTimeout must stay unset: a whole-request deadline
	// also applies to hijacked connections and would silently break WebSockets
	// and SSE.
	if cfg.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %s, want 0 so streaming keeps working", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %s, want 0 so streaming keeps working", cfg.WriteTimeout)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want %s", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty so ClientIP cannot be forged", cfg.TrustedProxies)
	}
}

func TestConfigKeepsExplicitValues(t *testing.T) {
	cfg := Config{MaxBodyBytes: 1024, Mode: "debug"}.withDefaults()

	if cfg.MaxBodyBytes != 1024 {
		t.Errorf("MaxBodyBytes = %d, want the configured 1024", cfg.MaxBodyBytes)
	}
	if cfg.Mode != "debug" {
		t.Errorf("Mode = %q, want the configured \"debug\"", cfg.Mode)
	}
}

// TestJSONFallbacksForUnmatchedRoutes closes a consistency gap: Gin answers an
// unmatched route with the plain text "404 page not found", so a client that
// parses JSON for every other error crashes on this one.
func TestJSONFallbacksForUnmatchedRoutes(t *testing.T) {
	type controller struct {
		List func(*gin.Context) `route:"GET:/users"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{List: okHandler("ok")})

	t.Run("unmatched path returns the JSON envelope", func(t *testing.T) {
		res := do(app, "GET", "/nope")

		if res.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.Code)
		}
		if got := res.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("Content-Type = %q, want JSON", got)
		}

		var body map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatalf("the 404 body is not JSON (%v): %s", err, res.Body.String())
		}
		if body["success"] != false {
			t.Errorf("success = %v, want false", body["success"])
		}
	})

	t.Run("unmatched method returns 405 as JSON", func(t *testing.T) {
		res := do(app, "DELETE", "/users")

		if res.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", res.Code)
		}
		if !strings.Contains(res.Body.String(), "METHOD_NOT_ALLOWED") {
			t.Errorf("body = %s, want METHOD_NOT_ALLOWED", res.Body.String())
		}
	})

	t.Run("fallbacks can be disabled", func(t *testing.T) {
		plain := newTestApp(Config{DisableJSONFallbacks: true})
		plain.RegisterControllers(&struct {
			Page func(*gin.Context) `route:"GET:/page"`
		}{Page: okHandler("ok")})

		res := do(plain, "GET", "/nope")
		if strings.Contains(res.Body.String(), "ROUTE_NOT_FOUND") {
			t.Errorf("body = %s, want Gin's plain 404 when fallbacks are disabled", res.Body.String())
		}
	})
}

// TestPanickingRequestsAreStillLogged pins the middleware ordering: the access
// log must sit outside recovery, or a panic unwinds past its logging and the one
// request you most want a log line for produces none.
func TestPanickingRequestsAreStillLogged(t *testing.T) {
	logs := silenceLogs(t)

	type controller struct {
		Boom func(*gin.Context) `route:"GET:/boom"`
	}

	app := newTestApp(Config{RequestLogger: true, RequestID: true})
	app.RegisterControllers(&controller{
		Boom: func(c *gin.Context) { panic("expected") },
	})

	do(app, "GET", "/boom")

	output := logs.String()
	if !strings.Contains(output, "panic recovered") {
		t.Error("the panic itself was not logged")
	}
	if !strings.Contains(output, `msg=request`) {
		t.Errorf("no access-log line for the panicking request:\n%s", output)
	}
	if !strings.Contains(output, "status=500") {
		t.Errorf("the access-log line does not record the 500:\n%s", output)
	}
}
