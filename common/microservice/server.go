package microservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// Config wires the microservice layer. Only the transport is required — every
// transport carries its own options, so setup really is "the transport and its
// options" and nothing else.
//
//	microservice.Setup(app, microservice.Config{
//	    Transport: redismq.New(redismq.Options{URL: "redis://localhost:6379"}),
//	})
type Config struct {
	// Transport is the transport to serve on. Use Transports to serve several.
	Transport Listener

	// Transports serves more than one transport in the same process; each
	// handler is routed by the `transport` tag matching a Listener's Name().
	Transports []Listener

	// Middleware runs before every message handler, in the order given. It is
	// separate from HTTP middleware because a message has no cookies, no CORS
	// and no client IP — reusing the HTTP chain here mostly runs no-ops.
	Middleware []gin.HandlerFunc

	// DisableRecovery removes the panic-recovery middleware from the message
	// engine. Leave it enabled: an escaping panic takes the process down, which
	// stops consuming every subject rather than only the one that failed.
	DisableRecovery bool

	// Concurrency caps how many messages are handled at once per transport.
	// Zero means DefaultConcurrency. Set it to 1 for strict per-partition
	// ordering.
	Concurrency int

	// HandlerTimeout bounds a single handler invocation. Without it one stuck
	// handler holds a concurrency slot forever and the consumer wedges.
	// Defaults to DefaultHandlerTimeout.
	HandlerTimeout time.Duration

	// Logger receives lifecycle and error events. Defaults to slog.Default().
	Logger *slog.Logger

	// OnError observes every dispatch failure. Use it for metrics; it must not
	// block.
	OnError func(pattern string, err error)
}

// Defaults applied to a zero Config.
const (
	DefaultConcurrency    = 64
	DefaultHandlerTimeout = 30 * time.Second
)

// Server owns the routers, the dispatcher and the transports.
type Server struct {
	cfg        Config
	app        *nika.App
	routers    map[string]*Router // transport name → router
	dispatch   *dispatcher
	listeners  map[string]Listener
	log        *slog.Logger
	timeout    time.Duration
	concurrent int

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
}

// Setup creates the microservice server, registers it in the DI container and
// arranges for it to start when the app starts and drain when the app stops.
//
// It deliberately does not begin consuming: handlers are registered by
// LoadModule, which usually runs after this call, and a consumer that started
// here would reject the first messages with "no handler registered".
func Setup(app *nika.App, cfg Config) (*Server, error) {
	if app == nil {
		return nil, errors.New("microservice: app is required")
	}

	listeners := make(map[string]Listener)
	appendListener := func(l Listener) error {
		if l == nil {
			return nil
		}
		name := l.Name()
		if name == "" {
			return errors.New("microservice: transport reported an empty name")
		}
		if _, duplicate := listeners[name]; duplicate {
			return fmt.Errorf("microservice: transport %q is configured twice", name)
		}
		listeners[name] = l
		return nil
	}

	if err := appendListener(cfg.Transport); err != nil {
		return nil, err
	}
	for _, l := range cfg.Transports {
		if err := appendListener(l); err != nil {
			return nil, err
		}
	}
	if len(listeners) == 0 {
		return nil, errors.New("microservice: at least one transport is required (Config.Transport or Config.Transports)")
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	timeout := cfg.HandlerTimeout
	if timeout <= 0 {
		timeout = DefaultHandlerTimeout
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	routers := make(map[string]*Router, len(listeners))
	for name := range listeners {
		routers[name] = NewRouter()
	}

	server := &Server{
		cfg:        cfg,
		app:        app,
		routers:    routers,
		listeners:  listeners,
		log:        log,
		timeout:    timeout,
		concurrent: concurrency,
	}
	server.dispatch = newDispatcher(cfg.Middleware, !cfg.DisableRecovery)

	app.RegisterSingleton(server)

	// Message handlers live on the same controllers as HTTP routes, so rather
	// than making the user register them twice, observe every controller the app
	// registers. Replay means Setup works before or after LoadModule.
	app.OnController(func(ctrl any) error {
		return server.registerController(ctrl)
	})

	app.OnStart(server.Start)
	app.OnShutdown(server.Stop)

	return server, nil
}

// RegisterControllers scans controllers for fields tagged
// `transport:"..." pattern:"..."` and registers each as a message handler.
//
//	type UserController struct {
//	    Create   func(*gin.Context) `transport:"redis" pattern:"user_created"`
//	    FindOne  func(*gin.Context) `transport:"redis" pattern:"user_*"`
//	    ListUser func(*gin.Context) `transport:"redis" pattern:"users"`
//	}
//
// A client that sends "user_created", "user_23" and "users" reaches Create,
// FindOne and ListUser respectively: the exact pattern wins over the wildcard
// even though "user_created" also matches "user_*".
//
// A field may carry both a `route` and a `transport` tag, in which case the same
// handler serves HTTP and messages.
func (s *Server) RegisterControllers(controllers ...any) error {
	for _, ctrl := range controllers {
		if err := s.registerController(ctrl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) registerController(ctrl any) error {
	if ctrl == nil {
		return errors.New("microservice: cannot register a nil controller")
	}

	val := reflect.ValueOf(ctrl)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return errors.New("microservice: cannot register a nil controller pointer")
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return fmt.Errorf("microservice: controller must be a struct or pointer to struct, got %s", val.Kind())
	}
	typ := val.Type()

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		transportName := field.Tag.Get("transport")
		if transportName == "" {
			continue
		}

		pattern := field.Tag.Get("pattern")
		if pattern == "" {
			return fmt.Errorf(
				"microservice: %s.%s has transport:%q but no pattern tag — add pattern:\"...\"",
				typ.Name(), field.Name, transportName,
			)
		}

		router, known := s.routers[transportName]
		if !known {
			return fmt.Errorf(
				"microservice: %s.%s declares transport %q, which is not configured (configured: %v)",
				typ.Name(), field.Name, transportName, s.transportNames(),
			)
		}

		handler, err := messageHandler(val.Field(i), field)
		if err != nil {
			return fmt.Errorf("microservice: %s.%s: %w", typ.Name(), field.Name, err)
		}

		guardTag := field.Tag.Get("guard")
		guards, guardNames, err := s.resolveGuards(guardTag)
		if err != nil {
			return fmt.Errorf("microservice: %s.%s: %w", typ.Name(), field.Name, err)
		}

		route := &Route{
			Pattern:    Pattern(pattern),
			Transport:  transportName,
			Controller: typ.Name(),
			Field:      field.Name,
			Guards:     guardNames,
		}
		if err := router.Add(route); err != nil {
			return err
		}

		chain := make([]gin.HandlerFunc, 0, len(guards)+1)
		chain = append(chain, guards...)
		chain = append(chain, handler)
		s.dispatch.mount(route, chain)

		s.log.Debug("microservice handler registered",
			slog.String("transport", transportName),
			slog.String("pattern", pattern),
			slog.String("handler", typ.Name()+"."+field.Name),
		)
	}

	return nil
}

// messageHandler extracts the gin handler from a tagged field.
func messageHandler(fieldVal reflect.Value, field reflect.StructField) (gin.HandlerFunc, error) {
	if field.Type.Kind() != reflect.Func {
		return nil, fmt.Errorf("message handler must be a func(*gin.Context), got %s", field.Type)
	}
	if !field.IsExported() {
		return nil, errors.New("message handler must be exported (start with an uppercase letter)")
	}
	if fieldVal.IsNil() {
		return nil, errors.New("message handler is nil — assign it in the controller constructor")
	}

	switch fn := fieldVal.Interface().(type) {
	case gin.HandlerFunc:
		return fn, nil
	case func(*gin.Context):
		return fn, nil
	default:
		return nil, fmt.Errorf("message handler must have signature func(*gin.Context), got %s", field.Type)
	}
}

// resolveGuards reuses the app's guard registry, so `guard:"Auth(admin)"` means
// the same thing on a message handler as on an HTTP route.
func (s *Server) resolveGuards(guardTag string) ([]gin.HandlerFunc, []string, error) {
	specs, err := nika.ParseGuardTag(guardTag)
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}

	handlers := make([]gin.HandlerFunc, 0, len(specs))
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		guardFn, exists := s.app.Guard(spec.Name)
		if !exists {
			return nil, nil, fmt.Errorf(
				"guard %q is not registered — call app.AddGuard(%q, ...) before loading modules",
				spec.Name, spec.Name,
			)
		}
		middleware := guardFn(spec.Args)
		if middleware == nil {
			return nil, nil, fmt.Errorf("guard %q returned a nil middleware", spec.Name)
		}
		handlers = append(handlers, middleware)
		names = append(names, spec.Name)
	}
	return handlers, names, nil
}

// Start begins consuming on every configured transport and returns immediately.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.mu.Unlock()

	total := 0
	for name, listener := range s.listeners {
		router := s.routers[name]
		patterns := router.Patterns()
		total += len(patterns)

		if len(patterns) == 0 {
			s.log.Warn("microservice transport has no handlers; not subscribing",
				slog.String("transport", name))
			continue
		}

		s.wg.Add(1)
		go s.runListener(runCtx, listener, router, patterns)
	}

	s.log.Info("microservice server started",
		slog.Int("handlers", total),
		slog.Any("transports", s.transportNames()),
	)
	return nil
}

// runListener supervises one transport, restarting it with backoff if it fails
// for a reason other than shutdown. A broker restart must not silently take the
// consumer offline for the lifetime of the process.
func (s *Server) runListener(ctx context.Context, listener Listener, router *Router, patterns []string) {
	defer s.wg.Done()

	dispatch := s.dispatcherFor(router)
	backoff := 250 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		err := listener.Listen(ctx, patterns, dispatch)

		if ctx.Err() != nil || errors.Is(err, ErrClosed) {
			return
		}
		if err == nil {
			// A clean return without cancellation means the transport decided it
			// is done; respect that rather than spinning.
			s.log.Info("microservice transport stopped", slog.String("transport", listener.Name()))
			return
		}

		s.log.Error("microservice transport failed; reconnecting",
			slog.String("transport", listener.Name()),
			slog.Duration("retry_in", backoff),
			slog.Any("error", err),
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// dispatcherFor returns the Dispatcher a transport hands its messages to. It
// enforces the per-handler timeout and the concurrency cap, so a transport
// implementation never has to.
func (s *Server) dispatcherFor(router *Router) Dispatcher {
	slots := make(chan struct{}, s.concurrent)

	return func(ctx context.Context, env *Envelope) (*Envelope, error) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		handlerCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()

		// Route through this transport's router, not a shared one, so the same
		// pattern can be served by different handlers on different transports.
		route, found := router.Resolve(env.Pattern)
		if !found {
			err := fmt.Errorf("%w: %q", ErrNoHandler, env.Pattern)
			s.reportError(env.Pattern, err)
			return replyError(env, 404, "PATTERN_NOT_FOUND",
				fmt.Sprintf("no handler is registered for pattern %q", env.Pattern)), nil
		}

		reply, err := s.dispatch.dispatchRoute(handlerCtx, env, route)
		if err != nil {
			s.reportError(env.Pattern, err)
			return reply, nil
		}
		if reply != nil && reply.Error != nil {
			s.reportError(env.Pattern, reply.Error)
		}
		return reply, nil
	}
}

func (s *Server) reportError(pattern string, err error) {
	if err == nil {
		return
	}
	s.log.Warn("microservice dispatch failed",
		slog.String("pattern", pattern),
		slog.Any("error", err),
	)
	if s.cfg.OnError != nil {
		s.cfg.OnError(pattern, err)
	}
}

// Listen starts the server and blocks until ctx is cancelled. Use it in a
// worker process that serves no HTTP.
func (s *Server) Listen(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return s.Stop(context.Background())
}

// Stop cancels the consumers, waits for in-flight handlers to finish within ctx,
// and closes every transport.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Wait for the listener goroutines, but never past the caller's deadline —
	// a broker that will not release its connection must not block shutdown.
	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
	case <-ctx.Done():
		s.log.Warn("microservice shutdown timed out waiting for consumers")
	}

	var firstErr error
	for name, listener := range s.listeners {
		if err := listener.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("microservice: closing %s: %w", name, err)
		}
	}

	s.log.Info("microservice server stopped")
	return firstErr
}

// Routes returns every registered message route, for logging or a health page.
func (s *Server) Routes() []*Route {
	var out []*Route
	for _, router := range s.routers {
		out = append(out, router.Routes()...)
	}
	return out
}

// Router returns the router for a transport, or nil when it is not configured.
func (s *Server) Router(transport string) *Router {
	return s.routers[transport]
}

func (s *Server) transportNames() []string {
	names := make([]string, 0, len(s.listeners))
	for name := range s.listeners {
		names = append(names, name)
	}
	return names
}
