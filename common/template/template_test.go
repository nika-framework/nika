package template

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// writeTemplates creates a directory of templates and returns its glob pattern.
func writeTemplates(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	return filepath.Join(dir, "*.html")
}

func TestSetupEErrorCases(t *testing.T) {
	cases := []struct {
		name        string
		pattern     func(t *testing.T) string
		app         func() *nika.App
		wantMessage string
	}{
		{
			name:        "glob matches nothing",
			pattern:     func(t *testing.T) string { return filepath.Join(t.TempDir(), "*.html") },
			wantMessage: "parse glob",
		},
		{
			name: "malformed template",
			pattern: func(t *testing.T) string {
				return writeTemplates(t, map[string]string{"broken.html": `{{ if }}`})
			},
			wantMessage: "parse glob",
		},
		{
			name:        "nil app",
			pattern:     func(t *testing.T) string { return filepath.Join(t.TempDir(), "*.html") },
			app:         func() *nika.App { return nil },
			wantMessage: "app must not be nil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := nika.NewApp(nika.Config{Mode: gin.TestMode})
			if tc.app != nil {
				app = tc.app()
			}

			got, err := SetupE(app, tc.pattern(t))
			if err == nil {
				t.Fatal("SetupE error = nil, want an error")
			}
			if got != nil {
				t.Fatal("SetupE returned a Template alongside the error")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("SetupE error = %q, want it to mention %q", err, tc.wantMessage)
			}
		})
	}
}

func TestSetupEHappyPathRendersAndAutoEscapes(t *testing.T) {
	pattern := writeTemplates(t, map[string]string{
		"hello.html": `<p>{{ .Name }}</p>`,
	})

	app := nika.NewApp(nika.Config{Mode: gin.TestMode})
	tmpl, err := SetupE(app, pattern)
	if err != nil {
		t.Fatalf("SetupE error = %v, want nil", err)
	}
	if tmpl == nil {
		t.Fatal("SetupE returned nil")
	}

	// Registered in the container so a controller can inject it and call Load.
	if resolved, ok := nika.Resolve[*Template](app); !ok || resolved != tmpl {
		t.Fatalf("Resolve[*Template] = (%v, %v), want the Template SetupE built", resolved, ok)
	}

	app.GET("/hello", func(c *gin.Context) {
		c.HTML(http.StatusOK, "hello.html", gin.H{"Name": `<script>alert(1)</script>`})
	})

	rec := httptest.NewRecorder()
	app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	// html/template must have escaped the interpolated value. If text/template were
	// substituted this assertion is what fails.
	if strings.Contains(body, "<script>") {
		t.Fatalf("body %q contains unescaped markup: html/template must not be replaced by text/template", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("body %q does not contain the escaped value", body)
	}
}

func TestSetupPanicsOnABadPattern(t *testing.T) {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Setup did not panic on a glob that matches nothing")
		}
	}()

	Setup(app, filepath.Join(t.TempDir(), "*.html"))
}

func TestSetupSucceedsOnAGoodPattern(t *testing.T) {
	pattern := writeTemplates(t, map[string]string{"page.html": `ok`})
	app := nika.NewApp(nika.Config{Mode: gin.TestMode})

	if got := Setup(app, pattern); got == nil {
		t.Fatal("Setup returned nil")
	}
}

func TestLoadAndLoadE(t *testing.T) {
	first := writeTemplates(t, map[string]string{"page.html": `first`})
	second := writeTemplates(t, map[string]string{"page.html": `second`})

	app := nika.NewApp(nika.Config{Mode: gin.TestMode})
	tmpl, err := SetupE(app, first)
	if err != nil {
		t.Fatalf("SetupE error = %v", err)
	}

	app.GET("/page", func(c *gin.Context) { c.HTML(http.StatusOK, "page.html", nil) })

	render := func() string {
		rec := httptest.NewRecorder()
		app.GetGin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/page", nil))
		return rec.Body.String()
	}

	if got := render(); got != "first" {
		t.Fatalf("body = %q, want %q", got, "first")
	}

	t.Run("LoadE replaces the set", func(t *testing.T) {
		if err := tmpl.LoadE(second); err != nil {
			t.Fatalf("LoadE error = %v, want nil", err)
		}
		if got := render(); got != "second" {
			t.Fatalf("body = %q, want %q", got, "second")
		}
	})

	t.Run("LoadE reports an error", func(t *testing.T) {
		err := tmpl.LoadE(filepath.Join(t.TempDir(), "*.html"))
		if err == nil {
			t.Fatal("LoadE error = nil, want an error")
		}
		if !strings.Contains(err.Error(), "parse glob") {
			t.Fatalf("LoadE error = %q, want it to mention the pattern", err)
		}
	})

	t.Run("Load panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Load did not panic on a bad pattern")
			}
		}()
		tmpl.Load(filepath.Join(t.TempDir(), "*.html"))
	})

	t.Run("Load succeeds", func(t *testing.T) {
		tmpl.Load(first)
		if got := render(); got != "first" {
			t.Fatalf("body = %q, want %q", got, "first")
		}
	})
}
