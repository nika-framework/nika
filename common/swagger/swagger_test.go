package swagger

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// withGinMode sets the global gin mode for one test and restores it afterwards.
func withGinMode(t *testing.T, mode string) {
	t.Helper()
	previous := gin.Mode()
	gin.SetMode(mode)
	t.Cleanup(func() { gin.SetMode(previous) })
}

func TestEnabledDefaultsToOffInReleaseMode(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		enabled *bool
		want    bool
	}{
		// The important case: a production build must not publish the API surface
		// just because this package was imported.
		{"release mode, unset", gin.ReleaseMode, nil, false},
		{"debug mode, unset", gin.DebugMode, nil, true},
		{"test mode, unset", gin.TestMode, nil, true},
		{"release mode, forced on", gin.ReleaseMode, Enable(true), true},
		{"debug mode, forced off", gin.DebugMode, Enable(false), false},
		{"test mode, forced off", gin.TestMode, Enable(false), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGinMode(t, tc.mode)
			if got := enabled(&Config{Enabled: tc.enabled}); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetupMountsOnlyWhenEnabled(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		cfg        *Config
		wantMount  bool
		wantedPath string
	}{
		{"release mode leaves the docs unmounted", gin.ReleaseMode, &Config{}, false, "/swagger/index.html"},
		{"debug mode mounts the default path", gin.DebugMode, &Config{}, true, "/swagger/index.html"},
		{"explicit path is honoured", gin.DebugMode, &Config{Path: "/docs/*any"}, true, "/docs/index.html"},
		{"nil config behaves like a zero config", gin.DebugMode, nil, true, "/swagger/index.html"},
		{"forced on in release mode", gin.ReleaseMode, &Config{Enabled: Enable(true)}, true, "/swagger/index.html"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGinMode(t, tc.mode)

			app := nika.NewApp(nika.Config{Mode: tc.mode})
			if got := Setup(app, tc.cfg); got != tc.wantMount {
				t.Fatalf("Setup = %v, want %v", got, tc.wantMount)
			}

			rec := httptest.NewRecorder()
			app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.wantedPath, nil))

			if tc.wantMount {
				if rec.Code == http.StatusNotFound {
					t.Fatalf("GET %s = 404, want the docs to be served", tc.wantedPath)
				}
				return
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404: the docs must not be reachable", tc.wantedPath, rec.Code)
			}
		})
	}
}

func TestBasePathNormalisesEveryAcceptedForm(t *testing.T) {
	cases := map[string]string{
		"":               "/swagger",
		"/docs":          "/docs",
		"/docs/":         "/docs",
		"/docs/*any":     "/docs",
		"/docs/*path":    "/docs",
		"docs":           "/docs",
		"/swagger/*any":  "/swagger",
		"/api/docs/*any": "/api/docs",
	}

	for path, want := range cases {
		if got := basePath(path); got != want {
			t.Errorf("basePath(%q) = %q, want %q", path, got, want)
		}
	}
}

// The UI lives at index.html and gin-swagger 404s anything else, so the bare
// path has to redirect — otherwise "/docs" looks like the docs are not mounted.
func TestSetupRedirectsTheBarePathToTheUI(t *testing.T) {
	withGinMode(t, gin.DebugMode)

	for _, configured := range []string{"/docs", "/docs/*any"} {
		t.Run(configured, func(t *testing.T) {
			app := nika.NewApp(nika.Config{Mode: gin.DebugMode})
			if !Setup(app, &Config{Path: configured}) {
				t.Fatal("Setup = false, want true")
			}

			for _, requested := range []string{"/docs", "/docs/"} {
				rec := httptest.NewRecorder()
				app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requested, nil))

				if rec.Code != http.StatusFound {
					t.Fatalf("GET %s = %d, want %d", requested, rec.Code, http.StatusFound)
				}
				if location := rec.Header().Get("Location"); location != "/docs/index.html" {
					t.Fatalf("GET %s redirected to %q, want /docs/index.html", requested, location)
				}
			}

			rec := httptest.NewRecorder()
			app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/index.html", nil))
			if !strings.Contains(rec.Body.String(), "swagger-ui") {
				t.Fatalf("GET /docs/index.html did not serve the UI: status %d", rec.Code)
			}
		})
	}
}

func TestSetupGuardsTheBarePathToo(t *testing.T) {
	withGinMode(t, gin.DebugMode)

	app := nika.NewApp(nika.Config{Mode: gin.DebugMode})
	Setup(app, &Config{
		Path:   "/docs",
		Guards: []gin.HandlerFunc{func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }},
	})

	// A redirect that skips the guard is a disclosure in itself: it confirms the
	// docs exist and hands out their real location.
	for _, path := range []string{"/docs", "/docs/", "/docs/index.html"} {
		rec := httptest.NewRecorder()
		app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusUnauthorized)
		}
	}
}

func TestSetupRunsGuardsBeforeTheDocsHandler(t *testing.T) {
	withGinMode(t, gin.DebugMode)

	app := nika.NewApp(nika.Config{Mode: gin.DebugMode})

	guardRan := false
	if !Setup(app, &Config{
		Guards: []gin.HandlerFunc{
			func(c *gin.Context) {
				guardRan = true
				if c.GetHeader("Authorization") == "" {
					c.AbortWithStatus(http.StatusUnauthorized)
					return
				}
				c.Next()
			},
		},
	}) {
		t.Fatal("Setup = false, want true")
	}

	t.Run("rejects an unauthenticated request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil))

		if !guardRan {
			t.Fatal("the guard did not run")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if strings.Contains(rec.Body.String(), "swagger-ui") {
			t.Fatal("the docs were served despite the guard aborting")
		}
	})

	t.Run("allows an authenticated request through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

		rec := httptest.NewRecorder()
		app.GetGin().ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Fatal("status = 401 for an authenticated request")
		}
	})
}

func TestEnableReturnsAUsablePointer(t *testing.T) {
	if on := Enable(true); on == nil || !*on {
		t.Fatalf("Enable(true) = %v, want a pointer to true", on)
	}
	if off := Enable(false); off == nil || *off {
		t.Fatalf("Enable(false) = %v, want a pointer to false", off)
	}
}
