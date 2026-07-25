package cors

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// TestNewRejectsCredentialsWithWildcardOrigins covers the unsafe combination: a
// wildcard Access-Control-Allow-Origin on a credentialed response would let any
// site on the internet read authenticated responses from the API.
func TestNewRejectsCredentialsWithWildcardOrigins(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "AllowAllOrigins with credentials",
			cfg: Config{
				AllowAllOrigins:  true,
				AllowCredentials: true,
			},
		},
		{
			name: "literal star entry with credentials",
			cfg: Config{
				AllowOrigins:     []string{"*"},
				AllowCredentials: true,
			},
		},
		{
			name: "star entry among real origins with credentials",
			cfg: Config{
				AllowOrigins:     []string{"https://app.example.com", "*"},
				AllowCredentials: true,
			},
		},
		{
			name: "padded star entry with credentials",
			cfg: Config{
				AllowOrigins:     []string{"  *  "},
				AllowCredentials: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.cfg)
			if err == nil {
				t.Fatal("New error = nil, want ErrCredentialsWithAllOrigins")
			}
			if !errors.Is(err, ErrCredentialsWithAllOrigins) {
				t.Fatalf("New error = %v, want ErrCredentialsWithAllOrigins", err)
			}
			if got != nil {
				t.Fatal("New returned a middleware alongside the error")
			}
		})
	}
}

func TestNewAllowsWildcardOriginsWithoutCredentials(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"AllowAllOrigins alone", Config{AllowAllOrigins: true}},
		{"star entry alone", Config{AllowOrigins: []string{"*"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err != nil {
				t.Fatalf("New error = %v, want nil: a public read-only API may allow every origin", err)
			}
		})
	}
}

func TestNewRejectsIneffectiveOrigins(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		wantMessage string
	}{
		{
			name:        "trailing slash",
			cfg:         Config{AllowOrigins: []string{"https://app.example.com/"}},
			wantMessage: "path",
		},
		{
			name:        "with a path",
			cfg:         Config{AllowOrigins: []string{"https://app.example.com/admin"}},
			wantMessage: "path",
		},
		{
			name:        "with a query string",
			cfg:         Config{AllowOrigins: []string{"https://app.example.com?x=1"}},
			wantMessage: "path",
		},
		{
			name:        "missing scheme",
			cfg:         Config{AllowOrigins: []string{"app.example.com"}},
			wantMessage: "full origin",
		},
		{
			name:        "empty entry",
			cfg:         Config{AllowOrigins: []string{"https://a.example.com", ""}},
			wantMessage: "empty entry",
		},
		{
			name:        "embedded credentials",
			cfg:         Config{AllowOrigins: []string{"https://user:pass@app.example.com"}},
			wantMessage: "credentials",
		},
		{
			name:        "wildcard without AllowWildcard",
			cfg:         Config{AllowOrigins: []string{"https://*.example.com"}},
			wantMessage: "AllowWildcard is false",
		},
		{
			name:        "two wildcards",
			cfg:         Config{AllowOrigins: []string{"https://*.example.*"}, AllowWildcard: true},
			wantMessage: "at most one",
		},
		{
			name:        "unparseable origin",
			cfg:         Config{AllowOrigins: []string{"https://exa mple.com"}},
			wantMessage: "not a valid origin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("New error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("New error = %q, want it to mention %q", err, tc.wantMessage)
			}
		})
	}
}

func TestNewAcceptsValidOrigins(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"https origin", Config{AllowOrigins: []string{"https://app.example.com"}}},
		{"http origin with a port", Config{AllowOrigins: []string{"http://localhost:3000"}}},
		{"several origins", Config{AllowOrigins: []string{"https://a.example.com", "http://localhost:5173"}}},
		{"wildcard when enabled", Config{AllowOrigins: []string{"https://*.example.com"}, AllowWildcard: true}},
		{"regexp form", Config{AllowOrigins: []string{`/^https://.+\.example\.com$/`}, AllowWildcard: true}},
		{"origin func", Config{AllowOriginFunc: func(string) bool { return true }}},
		{"websocket schema", Config{AllowOrigins: []string{"wss://ws.example.com"}, AllowWebSockets: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.cfg)
			if err != nil {
				t.Fatalf("New error = %v, want nil", err)
			}
			if c == nil || c.Middleware() == nil {
				t.Fatal("New returned no usable middleware")
			}
		})
	}
}

// TestNewReturnsAnErrorInsteadOfPanicking guards against the library's habit of
// panicking on a bad configuration during bootstrap.
func TestNewReturnsAnErrorInsteadOfPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("New panicked: %v", r)
		}
	}()

	// A zero Config configures no origins at all, which gin-contrib rejects by
	// panicking inside newCors.
	if _, err := New(Config{}); err == nil {
		t.Fatal("New(Config{}) error = nil, want an error")
	}
}

// serve runs one request through a router carrying the CORS middleware.
func serve(t *testing.T, cfg Config, method, origin string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New error = %v, want nil", err)
	}

	router := gin.New()
	router.Use(c.Middleware())
	router.GET("/ping", func(ctx *gin.Context) { ctx.String(http.StatusOK, "pong") })

	req := httptest.NewRequest(method, "/ping", nil)
	req.Host = "api.example.com"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareSetsTheExpectedHeaders(t *testing.T) {
	cfg := Config{
		AllowOrigins:     []string{"https://app.example.com"},
		AllowMethods:     []string{"GET", "POST"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"X-Total-Count"},
		AllowCredentials: true,
		MaxAge:           time.Hour,
	}

	t.Run("allowed origin on a simple request", func(t *testing.T) {
		rec := serve(t, cfg, http.MethodGet, "https://app.example.com", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want the requesting origin", got)
		}
		// The exact origin, never "*", is what makes credentials safe.
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
			t.Fatal("Access-Control-Allow-Origin = *, which must never be paired with credentials")
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Fatalf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
		}
		if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Total-Count") {
			t.Fatalf("Access-Control-Expose-Headers = %q, want it to contain X-Total-Count", got)
		}
		if rec.Body.String() != "pong" {
			t.Fatalf("body = %q, want the handler to have run", rec.Body.String())
		}
	})

	t.Run("disallowed origin is refused", func(t *testing.T) {
		rec := serve(t, cfg, http.MethodGet, "https://evil.example.net", nil)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want it unset for a rejected origin", got)
		}
		if strings.Contains(rec.Body.String(), "pong") {
			t.Fatal("the handler ran for a rejected origin")
		}
	})

	t.Run("preflight", func(t *testing.T) {
		rec := serve(t, cfg, http.MethodOptions, "https://app.example.com", map[string]string{
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "Authorization",
		})

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
			t.Fatalf("Access-Control-Allow-Methods = %q, want it to contain POST", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
			t.Fatalf("Access-Control-Allow-Headers = %q, want it to contain Authorization", got)
		}
		if got := rec.Header().Get("Access-Control-Max-Age"); got != "3600" {
			t.Fatalf("Access-Control-Max-Age = %q, want %q", got, "3600")
		}
	})

	t.Run("request without an Origin header is untouched", func(t *testing.T) {
		rec := serve(t, cfg, http.MethodGet, "", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want it unset for a non-CORS request", got)
		}
	})
}

func TestMiddlewareWithAllOriginsAndNoCredentials(t *testing.T) {
	rec := serve(t, Config{AllowAllOrigins: true}, http.MethodGet, "https://anywhere.example.net", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	// Without credentials, a wildcard exposes nothing that was not already public.
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want it unset", got)
	}
}

func TestSetupInstallsTheMiddlewareAndForwardsErrors(t *testing.T) {
	t.Run("rejects an unsafe configuration", func(t *testing.T) {
		got, err := Setup(nil, Config{AllowAllOrigins: true, AllowCredentials: true})
		if !errors.Is(err, ErrCredentialsWithAllOrigins) {
			t.Fatalf("Setup error = %v, want ErrCredentialsWithAllOrigins", err)
		}
		if got != nil {
			t.Fatal("Setup returned a module alongside the error")
		}
		// Validation must happen before the app is touched, so a nil app is safe on
		// the error path.
	})
}

func TestValidateOriginTable(t *testing.T) {
	cases := []struct {
		origin  string
		cfg     Config
		wantErr bool
	}{
		{"https://a.example.com", Config{}, false},
		{"http://localhost:8080", Config{}, false},
		{"*", Config{}, false},
		{" ", Config{}, true},
		{"", Config{}, true},
		{"https://a.example.com/", Config{}, true},
		{"//a.example.com", Config{}, true},
		{"ftp://a.example.com", Config{}, false}, // shape is fine; the library rejects the schema
		{"https://*.a.com", Config{AllowWildcard: true}, false},
		{"https://*.a.com", Config{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.origin, func(t *testing.T) {
			err := validateOrigin(tc.origin, tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOrigin(%q) error = %v, wantErr %v", tc.origin, err, tc.wantErr)
			}
		})
	}
}
