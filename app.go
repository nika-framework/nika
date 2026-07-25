package nika

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/gin-gonic/gin"
)

// GuardFunc builds a middleware from the arguments declared in a `guard` tag.
type GuardFunc func(args []string) gin.HandlerFunc

// App is the Nika application: a Gin engine, a dependency-injection container,
// and the module graph loaded into them.
//
// An App is safe for concurrent use. Registration methods take a write lock and
// resolution takes a read lock, so providers may be registered from goroutines
// during bootstrap without racing the request path.
type App struct {
	engine *gin.Engine
	cfg    Config

	mu             sync.RWMutex
	container      map[reflect.Type]any
	guards         map[string]GuardFunc
	moduleExports  map[reflect.Type]map[reflect.Type]any
	loadingModules map[reflect.Type]struct{}

	serverMu sync.Mutex
	server   *http.Server

	hookMu       sync.Mutex
	onStart      []func(context.Context) error
	onShutdown   []func(context.Context) error
	onController []func(any) error
	controllers  []any
	routeMethod  map[string]struct{}
	started      bool
}

// NewApp creates an application with hardened defaults. Pass a Config to
// override any of them; see Config for what each default is and why.
func NewApp(config ...Config) *App {
	var cfg Config
	if len(config) > 0 {
		cfg = config[0]
	}
	cfg = cfg.withDefaults()

	gin.SetMode(cfg.Mode)

	app := &App{
		engine:         gin.New(),
		cfg:            cfg,
		container:      make(map[reflect.Type]any),
		guards:         make(map[string]GuardFunc),
		moduleExports:  make(map[reflect.Type]map[reflect.Type]any),
		loadingModules: make(map[reflect.Type]struct{}),
		routeMethod:    make(map[string]struct{}),
	}

	// Never inherit Gin's default of trusting every proxy: that would let any
	// client forge X-Forwarded-For and defeat IP-based rate limiting and
	// allow-lists. An empty list makes ClientIP() report the socket peer.
	_ = app.engine.SetTrustedProxies(cfg.TrustedProxies)
	if cfg.TrustedPlatform != "" {
		app.engine.TrustedPlatform = cfg.TrustedPlatform
	}

	app.installBaseMiddleware()

	return app
}

// Config returns the effective configuration, with defaults applied.
func (a *App) Config() Config {
	return a.cfg
}

// installBaseMiddleware wires the always-on protections. The order is load
// bearing:
//
//   - request id first, so every later log line and error response carries it;
//   - the access log *outside* recovery, because its logging happens after
//     c.Next() returns — a panic would unwind straight past it and a crashing
//     request would produce no access-log line at all, which is exactly the
//     request you most want logged;
//   - recovery next, covering every handler downstream;
//   - then response hardening and the body cap.
func (a *App) installBaseMiddleware() {
	if a.cfg.RequestID {
		a.engine.Use(RequestIDMiddleware())
	}
	if a.cfg.RequestLogger {
		a.engine.Use(LoggerMiddleware())
	}
	if !a.cfg.DisableRecovery {
		a.engine.Use(RecoveryMiddleware())
	}
	if a.cfg.SecurityHeaders {
		a.engine.Use(SecurityHeadersMiddleware())
	}
	if !a.cfg.DisableBodyLimit {
		a.engine.Use(BodyLimitMiddleware(a.cfg.MaxBodyBytes))
	}

	if !a.cfg.DisableJSONFallbacks {
		a.installFallbackHandlers()
	}
}

// installFallbackHandlers replaces Gin's plain-text 404 and 405 bodies with the
// framework's JSON envelope.
//
// An API that answers JSON everywhere except "route not found" forces every
// client to parse two formats and handle the surprise, usually by crashing on
// `404 page not found` where it expected an object.
func (a *App) installFallbackHandlers() {
	a.engine.HandleMethodNotAllowed = true

	a.engine.NoRoute(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"success": false,
			"error": gin.H{
				"code":    http.StatusNotFound,
				"message": "ROUTE_NOT_FOUND",
				"details": c.Request.Method + " " + c.Request.URL.Path + " is not a registered route",
			},
		})
	})

	a.engine.NoMethod(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusMethodNotAllowed, gin.H{
			"success": false,
			"error": gin.H{
				"code":    http.StatusMethodNotAllowed,
				"message": "METHOD_NOT_ALLOWED",
				"details": c.Request.Method + " is not allowed on " + c.Request.URL.Path,
			},
		})
	})
}

// OnController registers an observer that receives every controller the app
// registers, including those registered before this call.
//
// Replaying past controllers is what makes setup order irrelevant: the
// microservice layer can be wired before or after LoadModule and still see the
// full set of handlers, instead of silently serving none because it was
// installed one line too late.
func (a *App) OnController(observe func(any) error) {
	if observe == nil {
		return
	}

	a.hookMu.Lock()
	existing := make([]any, len(a.controllers))
	copy(existing, a.controllers)
	a.onController = append(a.onController, observe)
	a.hookMu.Unlock()

	for _, ctrl := range existing {
		if err := observe(ctrl); err != nil {
			panic(fmt.Sprintf("nika: controller observer failed: %v", err))
		}
	}
}

// notifyControllerObservers records a controller and feeds it to the observers.
func (a *App) notifyControllerObservers(ctrl any) {
	a.hookMu.Lock()
	a.controllers = append(a.controllers, ctrl)
	observers := make([]func(any) error, len(a.onController))
	copy(observers, a.onController)
	a.hookMu.Unlock()

	for _, observe := range observers {
		if err := observe(ctrl); err != nil {
			panic(fmt.Sprintf("nika: controller observer failed: %v", err))
		}
	}
}

// OnStart registers a function to run once, just before the app begins serving.
//
// Background workers — message-transport consumers, schedulers, warm-up jobs —
// belong here rather than in a Setup call, because a consumer started during
// setup would begin dispatching messages before LoadModule has registered the
// handlers that serve them.
func (a *App) OnStart(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	a.hookMu.Lock()
	a.onStart = append(a.onStart, fn)
	a.hookMu.Unlock()
}

// Start runs the registered start hooks. Listen calls it automatically; call it
// directly in a process that has no HTTP listener, such as a pure worker.
//
// It is idempotent: a second call is a no-op, so a worker that calls Start and
// then Listen does not start its consumers twice.
func (a *App) Start(ctx context.Context) error {
	a.hookMu.Lock()
	if a.started {
		a.hookMu.Unlock()
		return nil
	}
	a.started = true
	hooks := make([]func(context.Context) error, len(a.onStart))
	copy(hooks, a.onStart)
	a.hookMu.Unlock()

	for _, hook := range hooks {
		if err := hook(ctx); err != nil {
			return err
		}
	}
	return nil
}

// OnShutdown registers a function to run while the app drains, before Listen
// returns. Hooks run in reverse registration order (last registered first), the
// same ordering as deferred cleanup, and each receives a context bounded by
// Config.ShutdownTimeout. Use it to close database and broker connections.
func (a *App) OnShutdown(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	a.hookMu.Lock()
	a.onShutdown = append(a.onShutdown, fn)
	a.hookMu.Unlock()
}

// runShutdownHooks executes every registered hook, collecting the first error
// but always running all of them so no connection is left dangling.
func (a *App) runShutdownHooks(ctx context.Context) error {
	a.hookMu.Lock()
	hooks := make([]func(context.Context) error, len(a.onShutdown))
	copy(hooks, a.onShutdown)
	a.hookMu.Unlock()

	var firstErr error
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
