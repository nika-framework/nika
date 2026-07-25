// Package nikatest provides the harness for testing a Nika application: it
// boots an app in-process, drives it through httptest without binding a port,
// and asserts on status, headers, JSON structure and rendered content.
//
// A test looks like this:
//
//	func TestCreateUser(t *testing.T) {
//	    app := nikatest.New(t)
//	    app.Override(&fakeUserRepo{})
//	    app.LoadModule(src.NewAppModule())
//
//	    app.POST("/users").
//	        JSON(map[string]any{"name": "Ada", "email": "ada@example.com"}).
//	        Do().
//	        ExpectStatus(201).
//	        ExpectJSONPath("data.name", "Ada")
//	}
//
// Every assertion calls Helper() so a failure is reported at the line of the
// assertion in your test, not inside this package.
package nikatest

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// TB is the subset of *testing.T this package needs.
//
// Depending on an interface rather than on *testing.T keeps `testing` out of the
// import graph of anything that imports this package, and lets the harness be
// driven from a benchmark, a fuzz target or a custom runner unchanged.
type TB interface {
	Helper()
	Cleanup(func())
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Name() string
	FailNow()
}

// App is an application under test.
type App struct {
	t   TB
	app *nika.App

	// overrides are DI registrations applied before modules load, so a module's
	// own provider can be replaced by a fake.
	overrides []any

	defaultHeaders map[string]string
	timeout        time.Duration
}

// Options configures the app under test.
type Options struct {
	// Config is passed through to nika.NewApp. The harness forces gin's test
	// mode and disables graceful shutdown regardless of what is set here.
	Config nika.Config

	// Timeout bounds a single request. Defaults to 10s so a deadlocked handler
	// fails the test instead of hanging the suite.
	Timeout time.Duration
}

// New boots a fresh app for the test, with gin in test mode.
func New(t TB, opts ...Options) *App {
	t.Helper()

	var options Options
	if len(opts) > 0 {
		options = opts[0]
	}

	cfg := options.Config
	cfg.Mode = gin.TestMode
	// A test must never install a signal handler or wait to drain: the process
	// running the suite owns those.
	cfg.DisableGracefulShutdown = true

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	app := &App{
		t:              t,
		app:            nika.NewApp(cfg),
		defaultHeaders: make(map[string]string, 4),
		timeout:        timeout,
	}

	// Run the shutdown hooks when the test ends so a fake repository, a memory
	// cache or a transport opened during the test is always released — a leaked
	// goroutine here shows up as a flake in an unrelated test.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.app.Shutdown(ctx)
	})

	return app
}

// Wrap adapts an already-built app — useful when production code owns the
// bootstrap and the test only wants to exercise it.
func Wrap(t TB, app *nika.App, opts ...Options) *App {
	t.Helper()

	var options Options
	if len(opts) > 0 {
		options = opts[0]
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &App{
		t:              t,
		app:            app,
		defaultHeaders: make(map[string]string, 4),
		timeout:        timeout,
	}
}

// Nika returns the underlying application, for anything the harness does not
// wrap.
func (a *App) Nika() *nika.App { return a.app }

// Override registers a provider that wins over whatever a module would provide
// for the same type.
//
// It must be called before LoadModule: module loading resolves dependencies
// against a snapshot of the container taken at that moment, so a fake registered
// afterwards would be ignored — silently, which is the failure mode this ordering
// requirement exists to prevent. Overriding after loading is reported as a test
// failure rather than accepted.
func (a *App) Override(instances ...any) *App {
	a.t.Helper()

	for _, instance := range instances {
		if instance == nil {
			a.t.Fatalf("nikatest: Override received a nil instance")
			return a
		}
		a.app.RegisterSingleton(instance)
		a.overrides = append(a.overrides, instance)
	}
	return a
}

// OverrideAs registers a fake under an interface type, which is how a fake
// repository replaces the real one when consumers depend on the interface.
func OverrideAs[Iface any](a *App, instance Iface) *App {
	a.t.Helper()
	nika.RegisterSingletonAs[Iface](a.app, instance)
	return a
}

// AddGuard registers a guard so `guard:"..."` tags resolve. Tests usually
// register a permissive stub; use StubGuard for that.
func (a *App) AddGuard(name string, guard nika.GuardFunc) *App {
	a.t.Helper()
	a.app.AddGuard(name, guard)
	return a
}

// StubGuard registers a guard that always allows the request through, so a test
// can exercise a protected route without building real credentials.
func (a *App) StubGuard(names ...string) *App {
	a.t.Helper()
	for _, name := range names {
		a.app.AddGuard(name, func([]string) gin.HandlerFunc {
			return func(c *gin.Context) { c.Next() }
		})
	}
	return a
}

// DenyGuard registers a guard that always rejects with the given status, for
// asserting that a route really is protected.
func (a *App) DenyGuard(name string, status int) *App {
	a.t.Helper()
	a.app.AddGuard(name, func([]string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.AbortWithStatusJSON(status, gin.H{
				"success": false,
				"error":   gin.H{"code": status, "message": "FORBIDDEN_BY_TEST_GUARD"},
			})
		}
	})
	return a
}

// LoadModule loads a module graph, failing the test on a bootstrap panic instead
// of aborting the whole suite.
func (a *App) LoadModule(module nika.Module) *App {
	a.t.Helper()

	if err := recoverAsError(func() { a.app.LoadModule(module) }); err != nil {
		a.t.Fatalf("nikatest: loading module %T failed: %v", module, err)
	}
	return a
}

// RegisterControllers registers controllers directly, for a test that exercises
// one controller without a surrounding module.
func (a *App) RegisterControllers(controllers ...any) *App {
	a.t.Helper()

	if err := recoverAsError(func() { a.app.RegisterControllers(controllers...) }); err != nil {
		a.t.Fatalf("nikatest: registering controllers failed: %v", err)
	}
	return a
}

// Use installs middleware on the app under test.
func (a *App) Use(middleware ...gin.HandlerFunc) *App {
	a.app.Use(middleware...)
	return a
}

// Start runs the app's start hooks, which is what launches message consumers.
func (a *App) Start() *App {
	a.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	if err := a.app.Start(ctx); err != nil {
		a.t.Fatalf("nikatest: start hooks failed: %v", err)
	}
	return a
}

// Header sets a header sent with every subsequent request — an auth token, an
// API version, a tenant id.
func (a *App) Header(key, value string) *App {
	a.defaultHeaders[key] = value
	return a
}

// BearerToken is shorthand for an Authorization header.
func (a *App) BearerToken(token string) *App {
	return a.Header("Authorization", "Bearer "+token)
}

// Timeout overrides the per-request deadline.
func (a *App) Timeout(d time.Duration) *App {
	if d > 0 {
		a.timeout = d
	}
	return a
}

// Resolve returns a provider from the container, failing the test when absent.
// Use it to reach into a service after driving it through the API.
func Resolve[T any](a *App) T {
	a.t.Helper()

	instance, ok := nika.Resolve[T](a.app)
	if !ok {
		a.t.Fatalf("nikatest: no provider registered for %s", reflect.TypeOf((*T)(nil)).Elem())
	}
	return instance
}

// Routes returns every registered route as "METHOD /path", so a test can assert
// the surface an app exposes — which is the cheapest way to catch an endpoint
// that was accidentally removed or exposed.
func (a *App) Routes() []string {
	return a.app.Routes()
}

// ExpectRoute fails the test when the route is not registered.
func (a *App) ExpectRoute(method, path string) *App {
	a.t.Helper()

	want := method + " " + path
	for _, route := range a.app.Routes() {
		if route == want {
			return a
		}
	}
	a.t.Errorf("nikatest: route %q is not registered.\n  registered: %v", want, a.app.Routes())
	return a
}

// ExpectNoRoute fails the test when the route *is* registered — the assertion
// that a debug or admin endpoint has not leaked into the build.
func (a *App) ExpectNoRoute(method, path string) *App {
	a.t.Helper()

	unwanted := method + " " + path
	for _, route := range a.app.Routes() {
		if route == unwanted {
			a.t.Errorf("nikatest: route %q should not be registered", unwanted)
			return a
		}
	}
	return a
}

// Handler returns the http.Handler under test, for use with an external client
// or httptest.NewServer when a real network hop is required (WebSockets, for
// instance, cannot be tested through a synthesized request).
func (a *App) Handler() http.Handler {
	return a.app.Handler()
}

// recoverAsError runs fn and converts a panic into an error, so a bootstrap
// failure fails one test rather than crashing the run.
func recoverAsError(fn func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if e, ok := recovered.(error); ok {
				err = e
				return
			}
			err = fmt.Errorf("%v", recovered)
		}
	}()
	fn()
	return nil
}
