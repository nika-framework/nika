package nika

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestApp builds an app with the middleware that would otherwise interfere
// with unit assertions left off, so each test opts into what it exercises.
func newTestApp(config ...Config) *App {
	var cfg Config
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg.Mode = gin.TestMode
	return NewApp(cfg)
}

// expectPanic is deferred by a test that expects a panic containing substr.
func expectPanic(t *testing.T, substr string) {
	t.Helper()

	recovered := recover()
	if recovered == nil {
		t.Fatalf("expected a panic containing %q, but the call returned normally", substr)
	}

	message := messageOf(recovered)
	if !strings.Contains(message, substr) {
		t.Fatalf("expected a panic containing %q, got: %s", substr, message)
	}
}

func messageOf(recovered any) string {
	if err, ok := recovered.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%v", recovered)
}

// do drives one request through an app and returns the recorder.
func do(app *App, method, path string, body ...string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if len(body) > 0 {
		reader = strings.NewReader(body[0])
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, reader)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, req)
	return recorder
}

// moduleSpec holds the four module lists. Loader tests embed it in a distinct
// named type each, because the loader memoises exports by module *type* — two
// values of one type would resolve to the first one's exports, which is correct
// behaviour but would make the tests describe each other rather than the code.
type moduleSpec struct {
	imports     []Module
	controllers []any
	providers   []any
	exports     []any
}

func (m moduleSpec) Imports() []Module  { return m.imports }
func (m moduleSpec) Controllers() []any { return m.controllers }
func (m moduleSpec) Providers() []any   { return m.providers }
func (m moduleSpec) Exports() []any     { return m.exports }

// okHandler is a handler that answers 200 with a fixed body.
func okHandler(body string) func(*gin.Context) {
	return func(c *gin.Context) {
		c.String(http.StatusOK, body)
	}
}
