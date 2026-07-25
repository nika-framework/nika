package nika

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- module fixtures ------------------------------------------------------
//
// Each module gets its own named type: the loader memoises exports by module
// type, so reusing one type across cases would have the tests describe each
// other instead of the loader.

type dbModule struct{ moduleSpec }
type userModule struct{ moduleSpec }
type orderModule struct{ moduleSpec }
type leafModule struct{ moduleSpec }

type cycleA struct{ moduleSpec }
type cycleB struct{ moduleSpec }

func (m cycleA) Imports() []Module { return []Module{cycleB{}} }
func (m cycleB) Imports() []Module { return []Module{cycleA{}} }

// --- tests ----------------------------------------------------------------

func TestLoadModuleWiresControllersAndProviders(t *testing.T) {
	type controller struct {
		Repo *repository
		List func(*gin.Context) `route:"GET:/users"`
	}

	ctrl := &controller{}
	ctrl.List = func(c *gin.Context) {
		c.String(http.StatusOK, ctrl.Repo.name)
	}

	app := newTestApp()
	app.LoadModule(userModule{moduleSpec{
		providers:   []any{&repository{name: "users-repo"}},
		controllers: []any{ctrl},
	}})

	if res := do(app, "GET", "/users"); res.Body.String() != "users-repo" {
		t.Errorf("GET /users = %q, want the injected repository name", res.Body.String())
	}
}

func TestLoadModuleInvokesControllerConstructors(t *testing.T) {
	type controller struct {
		List func(*gin.Context) `route:"GET:/orders"`
	}

	app := newTestApp()
	app.LoadModule(orderModule{moduleSpec{
		providers: []any{&repository{name: "orders-repo"}},
		controllers: []any{
			func(repo *repository) *controller {
				return &controller{
					List: func(c *gin.Context) { c.String(http.StatusOK, repo.name) },
				}
			},
		},
	}})

	if res := do(app, "GET", "/orders"); res.Body.String() != "orders-repo" {
		t.Errorf("GET /orders = %q, want \"orders-repo\"", res.Body.String())
	}
}

func TestExportsCrossModuleBoundaries(t *testing.T) {
	type controller struct {
		List func(*gin.Context) `route:"GET:/consumers"`
	}

	shared := &repository{name: "shared"}

	app := newTestApp()
	app.LoadModule(userModule{moduleSpec{
		imports: []Module{
			dbModule{moduleSpec{
				providers: []any{shared},
				exports:   []any{shared},
			}},
		},
		controllers: []any{
			func(repo *repository) *controller {
				return &controller{
					List: func(c *gin.Context) { c.String(http.StatusOK, repo.name) },
				}
			},
		},
	}})

	if res := do(app, "GET", "/consumers"); res.Body.String() != "shared" {
		t.Errorf("GET /consumers = %q, want the exported provider", res.Body.String())
	}
}

func TestUnexportedProviderStaysPrivate(t *testing.T) {
	type controller struct {
		List func(*gin.Context) `route:"GET:/x"`
	}

	app := newTestApp()

	// dbModule provides a repository but exports nothing, so the importing
	// module must not be able to resolve it.
	defer expectPanic(t, "cannot resolve")
	app.LoadModule(userModule{moduleSpec{
		imports: []Module{
			dbModule{moduleSpec{providers: []any{&repository{name: "private"}}}},
		},
		controllers: []any{
			func(repo *repository) *controller { return &controller{List: okHandler("x")} },
		},
	}})
}

func TestExportingAnUnavailableProviderFails(t *testing.T) {
	app := newTestApp()

	defer expectPanic(t, "which it neither provides nor imports")
	app.LoadModule(leafModule{moduleSpec{
		exports: []any{&repository{name: "never-provided"}},
	}})
}

func TestCircularImportIsReported(t *testing.T) {
	app := newTestApp()

	defer expectPanic(t, "circular module import")
	app.LoadModule(cycleA{})
}

func TestLoadModuleIsIdempotentPerType(t *testing.T) {
	built := 0

	app := newTestApp()
	shared := dbModule{moduleSpec{
		providers: []any{
			func() *repository {
				built++
				return &repository{name: "counted"}
			},
		},
		exports: []any{func() *repository { return nil }},
	}}

	// Two modules importing the same one must share a single instance rather
	// than each constructing their own.
	app.LoadModule(userModule{moduleSpec{imports: []Module{shared}}})
	app.LoadModule(orderModule{moduleSpec{imports: []Module{shared}}})

	if built != 1 {
		t.Errorf("the shared provider was constructed %d times, want 1", built)
	}
}

func TestLoadModuleRejectsNil(t *testing.T) {
	app := newTestApp()
	defer expectPanic(t, "module cannot be nil")
	app.LoadModule(nil)
}

func TestConstructorReturningAnInterfaceIsIndexedAsOne(t *testing.T) {
	type controller struct {
		Hello func(*gin.Context) `route:"GET:/hello"`
	}

	app := newTestApp()
	app.LoadModule(leafModule{moduleSpec{
		providers: []any{
			func() greeter { return englishGreeter{} },
		},
		controllers: []any{
			func(g greeter) *controller {
				return &controller{
					Hello: func(c *gin.Context) { c.String(http.StatusOK, g.Greet()) },
				}
			},
		},
	}})

	if res := do(app, "GET", "/hello"); res.Body.String() != "hello" {
		t.Errorf("GET /hello = %q, want \"hello\"", res.Body.String())
	}
}

// --- lifecycle ------------------------------------------------------------

func TestStartHooksRunOnceInOrder(t *testing.T) {
	var order []string

	app := newTestApp()
	app.OnStart(func(context.Context) error {
		order = append(order, "first")
		return nil
	})
	app.OnStart(func(context.Context) error {
		order = append(order, "second")
		return nil
	})

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start() returned %v", err)
	}
	// A worker that calls Start and then Listen must not start its consumers
	// twice.
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("the second Start() returned %v", err)
	}

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("start hooks ran %v, want [first second] exactly once", order)
	}
}

func TestStartHookErrorAborts(t *testing.T) {
	app := newTestApp()

	ran := false
	app.OnStart(func(context.Context) error { return errors.New("broker unreachable") })
	app.OnStart(func(context.Context) error {
		ran = true
		return nil
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("Start() returned nil, want the hook error")
	}
	if ran {
		t.Error("a later start hook ran after an earlier one failed")
	}
}

func TestShutdownHooksRunInReverseOrder(t *testing.T) {
	var order []string

	app := newTestApp()
	app.OnShutdown(func(context.Context) error {
		order = append(order, "db")
		return nil
	})
	app.OnShutdown(func(context.Context) error {
		order = append(order, "broker")
		return nil
	})

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() returned %v", err)
	}

	// Reverse registration order mirrors defer, so a resource is torn down
	// before the thing it depends on.
	if len(order) != 2 || order[0] != "broker" || order[1] != "db" {
		t.Errorf("shutdown hooks ran %v, want [broker db]", order)
	}
}

func TestShutdownRunsEveryHookEvenAfterAFailure(t *testing.T) {
	app := newTestApp()

	var mu sync.Mutex
	ran := 0
	record := func(context.Context) error {
		mu.Lock()
		ran++
		mu.Unlock()
		return nil
	}

	app.OnShutdown(record)
	app.OnShutdown(func(context.Context) error { return errors.New("close failed") })
	app.OnShutdown(record)

	err := app.Shutdown(context.Background())
	if err == nil {
		t.Error("Shutdown() returned nil, want the first hook error")
	}
	// A hook that fails must not leave later connections dangling.
	if ran != 2 {
		t.Errorf("%d of 2 healthy hooks ran, want both", ran)
	}
}

// TestServeDrainsOnShutdown exercises the real server path: bind port 0, serve,
// then shut down from another goroutine and confirm both the listener and the
// hooks were dealt with.
func TestServeDrainsOnShutdown(t *testing.T) {
	type controller struct {
		Ping func(*gin.Context) `route:"GET:/ping"`
	}

	app := newTestApp(Config{DisableGracefulShutdown: true})
	app.RegisterControllers(&controller{Ping: okHandler("pong")})

	closed := false
	app.OnShutdown(func(context.Context) error {
		closed = true
		return nil
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot bind a test listener: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- app.Serve(listener) }()

	// The server is up once a request round-trips.
	client := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + listener.Addr().String() + "/ping"

	var body string
	for attempt := 0; attempt < 50; attempt++ {
		res, err := client.Get(url)
		if err == nil {
			buf := make([]byte, 16)
			n, _ := res.Body.Read(buf)
			body = string(buf[:n])
			res.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body != "pong" {
		t.Fatalf("GET /ping over the real listener returned %q, want \"pong\"", body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown() returned %v", err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve() returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Serve() did not return after Shutdown()")
	}

	if !closed {
		t.Error("the shutdown hook did not run")
	}
}

func TestNewServerAppliesTimeouts(t *testing.T) {
	app := newTestApp(Config{
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       7 * time.Second,
		MaxHeaderBytes:    4096,
	})

	server := app.newServer(":0")

	// ReadHeaderTimeout is the Slowloris defence; gin's Run sets none of these.
	if server.ReadHeaderTimeout != 3*time.Second {
		t.Errorf("ReadHeaderTimeout = %s, want 3s", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout != 7*time.Second {
		t.Errorf("IdleTimeout = %s, want 7s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 4096 {
		t.Errorf("MaxHeaderBytes = %d, want 4096", server.MaxHeaderBytes)
	}
	if server.Handler == nil {
		t.Error("the server has no handler")
	}
}
