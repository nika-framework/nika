package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	"github.com/ulule/limiter/v3"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestNewValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "zero requests", cfg: Config{Window: time.Minute}, wantErr: "requests"},
		{name: "negative requests", cfg: Config{Requests: -1, Window: time.Minute}, wantErr: "requests"},
		{name: "zero window", cfg: Config{Requests: 10}, wantErr: "window"},
		{name: "negative window", cfg: Config{Requests: 10, Window: -time.Second}, wantErr: "window"},
		{name: "unknown driver", cfg: Config{Requests: 10, Window: time.Minute, Driver: "mongo"}, wantErr: "driver"},
		{
			// Naming the redis driver without a client would otherwise produce a
			// limiter that fails every request at runtime instead of at startup.
			name:    "redis driver without a client",
			cfg:     Config{Requests: 10, Window: time.Minute, Driver: DriverRedis},
			wantErr: "redis client",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.cfg)
			if err == nil {
				t.Fatalf("New(%+v) returned nil, want an error mentioning %q", test.cfg, test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}

func TestNewAcceptsAValidConfig(t *testing.T) {
	rl, err := New(Config{Requests: 10, Window: time.Minute})
	if err != nil {
		t.Fatalf("New returned %v", err)
	}
	if rl.Middleware() == nil {
		t.Error("Middleware() is nil")
	}
	if rl.Limiter == nil {
		t.Error("Limiter is nil")
	}
	if got := rl.Limiter.Rate.Limit; got != 10 {
		t.Errorf("Rate.Limit = %d, want 10", got)
	}
	if got := rl.Limiter.Rate.Period; got != time.Minute {
		t.Errorf("Rate.Period = %s, want 1m", got)
	}
}

// serve builds an app with the limiter installed and returns a request driver.
// trustedProxies is separate from Config because proxy trust belongs to the app,
// not to the limiter — which is exactly why the limiter inherits it.
func serve(t *testing.T, cfg Config, trustedProxies ...string) func(remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	rl, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned %v", err)
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, TrustedProxies: trustedProxies})
	app.Use(rl.Middleware())
	app.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	return func(remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, req)
		return recorder
	}
}

func TestLimitIsEnforcedPerClient(t *testing.T) {
	do := serve(t, Config{Requests: 3, Window: time.Minute})

	for attempt := 1; attempt <= 3; attempt++ {
		if got := do("10.0.0.1:1000", nil).Code; got != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (within the limit)", attempt, got)
		}
	}

	res := do("10.0.0.1:1000", nil)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("the 4th request = %d, want 429", res.Code)
	}
	// The rejection must use the framework's error envelope, so a client parses
	// one shape for every failure.
	body := res.Body.String()
	if !strings.Contains(body, "RATE_LIMIT_ERROR") {
		t.Errorf("429 body = %s, want the framework error envelope", body)
	}
	if !strings.Contains(body, `"success":false`) {
		t.Errorf("429 body = %s, want success:false", body)
	}

	// A different client must have its own budget.
	if got := do("10.0.0.2:1000", nil).Code; got != http.StatusOK {
		t.Errorf("a different client = %d, want 200 — the limit is not per-client", got)
	}
}

// TestForgedForwardedHeaderCannotResetTheBudget is the reason the framework
// stopped trusting every proxy: the limiter keys on ClientIP(), so if
// X-Forwarded-For were trusted by default, any client could bypass the limit
// entirely by sending a fresh value on each request.
func TestForgedForwardedHeaderCannotResetTheBudget(t *testing.T) {
	do := serve(t, Config{Requests: 2, Window: time.Minute})

	do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "1.1.1.1"})
	do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "2.2.2.2"})

	res := do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "3.3.3.3"})
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("the 3rd request with a rotating X-Forwarded-For = %d, want 429 — the limit was bypassed",
			res.Code)
	}
}

func TestTrustedProxyHeaderIsHonoured(t *testing.T) {
	// Behind a real load balancer the forwarded address *is* the client, so once
	// the proxy is declared trusted each forwarded IP gets its own budget.
	do := serve(t, Config{Requests: 1, Window: time.Minute}, "10.0.0.0/8")

	if got := do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "1.1.1.1"}).Code; got != http.StatusOK {
		t.Fatalf("the first forwarded client = %d, want 200", got)
	}
	if got := do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "1.1.1.1"}).Code; got != http.StatusTooManyRequests {
		t.Errorf("the same forwarded client twice = %d, want 429", got)
	}
	if got := do("10.0.0.9:1000", map[string]string{"X-Forwarded-For": "2.2.2.2"}).Code; got != http.StatusOK {
		t.Errorf("a different forwarded client = %d, want 200", got)
	}
}

func TestCustomKeyFunc(t *testing.T) {
	// Keying on an API token rather than an IP is the common requirement, and it
	// must beat the IP default even when both clients share an address.
	do := serve(t, Config{
		Requests: 1,
		Window:   time.Minute,
		KeyFunc:  func(c *gin.Context) string { return c.GetHeader("X-API-Key") },
	})

	if got := do("10.0.0.1:1000", map[string]string{"X-API-Key": "key-a"}).Code; got != http.StatusOK {
		t.Fatalf("the first request for key-a = %d, want 200", got)
	}
	if got := do("10.0.0.2:2000", map[string]string{"X-API-Key": "key-a"}).Code; got != http.StatusTooManyRequests {
		t.Errorf("the second request for key-a from another IP = %d, want 429", got)
	}
	if got := do("10.0.0.1:1000", map[string]string{"X-API-Key": "key-b"}).Code; got != http.StatusOK {
		t.Errorf("the first request for key-b = %d, want 200", got)
	}
}

func TestSkipBypassesTheLimiter(t *testing.T) {
	do := serve(t, Config{
		Requests: 1,
		Window:   time.Minute,
		Skip:     func(c *gin.Context) bool { return c.GetHeader("X-Internal") == "yes" },
	})

	// A skipped request must neither be rejected nor consume budget.
	for attempt := 1; attempt <= 5; attempt++ {
		if got := do("10.0.0.1:1000", map[string]string{"X-Internal": "yes"}).Code; got != http.StatusOK {
			t.Fatalf("skipped request %d = %d, want 200", attempt, got)
		}
	}
	if got := do("10.0.0.1:1000", nil).Code; got != http.StatusOK {
		t.Errorf("the first counted request = %d, want 200 — skipped requests consumed budget", got)
	}
}

func TestCustomStatusAndMessage(t *testing.T) {
	do := serve(t, Config{
		Requests:   1,
		Window:     time.Minute,
		StatusCode: http.StatusServiceUnavailable,
		Message:    "slow down please",
	})

	do("10.0.0.1:1000", nil)
	res := do("10.0.0.1:1000", nil)

	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the configured 503", res.Code)
	}
	if !strings.Contains(res.Body.String(), "slow down please") {
		t.Errorf("body = %s, want the configured message", res.Body.String())
	}
}

func TestWindowExpiryRestoresBudget(t *testing.T) {
	// A short window keeps the test fast while still exercising the real
	// expiry path rather than a mocked clock.
	do := serve(t, Config{Requests: 1, Window: 150 * time.Millisecond})

	if got := do("10.0.0.1:1000", nil).Code; got != http.StatusOK {
		t.Fatalf("the first request = %d, want 200", got)
	}
	if got := do("10.0.0.1:1000", nil).Code; got != http.StatusTooManyRequests {
		t.Fatalf("the second request = %d, want 429", got)
	}

	time.Sleep(250 * time.Millisecond)

	if got := do("10.0.0.1:1000", nil).Code; got != http.StatusOK {
		t.Errorf("after the window expired = %d, want 200", got)
	}
}

func TestRateLimitHeadersAreSet(t *testing.T) {
	do := serve(t, Config{Requests: 5, Window: time.Minute})
	res := do("10.0.0.1:1000", nil)

	// Without these a client cannot back off before being rejected.
	for _, header := range []string{"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"} {
		if res.Header().Get(header) == "" {
			t.Errorf("%s is not set", header)
		}
	}
	if got := res.Header().Get("X-Ratelimit-Limit"); got != "5" {
		t.Errorf("X-Ratelimit-Limit = %q, want \"5\"", got)
	}
}

func TestSetupRegistersInTheContainer(t *testing.T) {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode})

	rl, err := Setup(app, Config{Requests: 10, Window: time.Minute})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}

	resolved, ok := nika.Resolve[*RateLimiter](app)
	if !ok {
		t.Fatal("*RateLimiter is not registered in the container")
	}
	if resolved != rl {
		t.Error("the container holds a different *RateLimiter than Setup returned")
	}
	if _, ok := nika.Resolve[*limiter.Limiter](app); !ok {
		t.Error("*limiter.Limiter is not registered in the container")
	}
}

func TestSetupPropagatesAConfigError(t *testing.T) {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode})

	if _, err := Setup(app, Config{}); err == nil {
		t.Error("Setup with an invalid config returned nil, want an error")
	}
}

func TestBuildStorePrefersAnExplicitStore(t *testing.T) {
	// An explicitly supplied store must win over the driver, which is how a
	// caller plugs in a store this package does not know about.
	provided, err := New(Config{Requests: 1, Window: time.Minute, Driver: DriverMemory})
	if err != nil {
		t.Fatalf("New returned %v", err)
	}

	reused, err := buildStore(Config{Store: provided.Limiter.Store, Driver: DriverRedis})
	if err != nil {
		t.Fatalf("buildStore returned %v", err)
	}
	if reused != provided.Limiter.Store {
		t.Error("buildStore did not return the explicitly provided store")
	}
}
