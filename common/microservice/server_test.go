package microservice

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// --- fixtures -------------------------------------------------------------

// UserController is the shape the framework is designed around: one struct
// whose fields declare a transport and a pattern, and nothing else.
type UserController struct {
	Create   func(*gin.Context) `transport:"memory" pattern:"user_created"`
	FindOne  func(*gin.Context) `transport:"memory" pattern:"user_*"`
	ListUser func(*gin.Context) `transport:"memory" pattern:"users"`
}

type CreateUserDto struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// calls records which handler ran, so a test can assert dispatch rather than
// only the reply body.
type calls struct {
	mu      sync.Mutex
	handler []string
	subject []string
}

func (c *calls) record(name, subject string) {
	c.mu.Lock()
	c.handler = append(c.handler, name)
	c.subject = append(c.subject, subject)
	c.mu.Unlock()
}

func (c *calls) handlers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.handler...)
}

func newUserController(record *calls) *UserController {
	return &UserController{
		Create: func(c *gin.Context) {
			var dto CreateUserDto
			if err := c.ShouldBindJSON(&dto); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"success": false,
					"error":   gin.H{"code": 400, "message": "INVALID_JSON"},
				})
				return
			}
			record.record("Create", PatternFrom(c))
			c.JSON(http.StatusCreated, gin.H{
				"success": true,
				"data":    gin.H{"id": "u1", "name": dto.Name},
			})
		},

		FindOne: func(c *gin.Context) {
			// A wildcard handler needs the literal subject the client sent to know
			// *which* user was asked for; that is what PatternFrom returns.
			subject := PatternFrom(c)
			record.record("FindOne", subject)

			id := strings.TrimPrefix(subject, "user_")
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data":    gin.H{"id": id},
			})
		},

		ListUser: func(c *gin.Context) {
			record.record("ListUser", PatternFrom(c))
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data":    []gin.H{{"id": "u1"}, {"id": "u2"}},
			})
		},
	}
}

// newServer boots an app with the in-memory transport and the controller wired.
func newServer(t *testing.T, controllers ...any) (*Server, *MemoryTransport, *nika.App) {
	t.Helper()

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := NewMemory()

	server, err := Setup(app, Config{Transport: transport})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}

	app.RegisterControllers(controllers...)

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})

	return server, transport, app
}

func dispatch(t *testing.T, transport *MemoryTransport, pattern string, payload any) *Envelope {
	t.Helper()

	env, err := NewEnvelope(pattern, payload)
	if err != nil {
		t.Fatalf("NewEnvelope(%q) returned %v", pattern, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	reply, err := transport.Dispatch(ctx, env)
	if err != nil {
		t.Fatalf("Dispatch(%q) returned %v", pattern, err)
	}
	return reply
}

// --- tests ----------------------------------------------------------------

// TestTaggedHandlersDispatchByPattern is the acceptance test for the whole
// design: the client sends "user_created", "user_23" and "users" and nothing
// else, and the three tagged handlers run in that order.
func TestTaggedHandlersDispatchByPattern(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	dispatch(t, transport, "user_created", CreateUserDto{Name: "Ada", Email: "ada@example.com"})
	dispatch(t, transport, "user_23", nil)
	dispatch(t, transport, "users", nil)

	got := record.handlers()
	want := []string{"Create", "FindOne", "ListUser"}

	if len(got) != len(want) {
		t.Fatalf("handlers ran %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("handlers ran %v, want %v", got, want)
		}
	}
}

// TestExactPatternWinsOverWildcard pins the precedence that makes the scenario
// work at all: "user_created" matches both "user_created" and "user_*".
func TestExactPatternWinsOverWildcard(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	dispatch(t, transport, "user_created", CreateUserDto{Name: "Ada"})

	if got := record.handlers(); len(got) != 1 || got[0] != "Create" {
		t.Errorf("\"user_created\" reached %v, want [Create] — the wildcard shadowed the exact pattern", got)
	}
}

func TestWildcardHandlerSeesTheLiteralSubject(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	reply := dispatch(t, transport, "user_42", nil)

	if reply.Status != http.StatusOK {
		t.Fatalf("reply status = %d, want 200: %s", reply.Status, reply.Data)
	}
	// The handler is registered as "user_*" but must be able to read "user_42",
	// otherwise a wildcard route cannot know what it was asked for.
	if !strings.Contains(string(reply.Data), `"id":"42"`) {
		t.Errorf("reply data = %s, want the id extracted from the literal subject", reply.Data)
	}
	if subject := record.subject[0]; subject != "user_42" {
		t.Errorf("PatternFrom(c) = %q, want the literal \"user_42\"", subject)
	}
}

// TestBindingAndValidationWorkInAHandler is the property that justified
// dispatching through a real gin engine: the exact same c.ShouldBindJSON a REST
// handler uses must work on a message.
func TestBindingAndValidationWorkInAHandler(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	t.Run("valid payload binds", func(t *testing.T) {
		reply := dispatch(t, transport, "user_created", CreateUserDto{Name: "Ada"})

		if reply.Status != http.StatusCreated {
			t.Fatalf("reply status = %d, want 201: %s", reply.Status, reply.Data)
		}
		if !strings.Contains(string(reply.Data), `"name":"Ada"`) {
			t.Errorf("reply data = %s, want the bound name", reply.Data)
		}
	})

	t.Run("malformed payload reaches the error path", func(t *testing.T) {
		env := &Envelope{ID: NewID(), Pattern: "user_created", Data: []byte(`{"name":`)}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		reply, err := transport.Dispatch(ctx, env)
		if err != nil {
			t.Fatalf("Dispatch returned %v", err)
		}
		if reply.Status != http.StatusBadRequest {
			t.Errorf("reply status = %d, want 400 for malformed JSON", reply.Status)
		}
		// A handler error must be surfaced as a structured envelope error, so a
		// caller can branch on it instead of parsing a body.
		if reply.Error == nil {
			t.Fatal("reply.Error is nil, want the handler's error")
		}
		if reply.Error.Message != "INVALID_JSON" {
			t.Errorf("reply.Error.Message = %q, want \"INVALID_JSON\"", reply.Error.Message)
		}
	})
}

func TestUnknownPatternIsRejectedNotDropped(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	reply := dispatch(t, transport, "order_created", nil)

	if reply.Status != http.StatusNotFound {
		t.Errorf("reply status = %d, want 404 for an unrouted subject", reply.Status)
	}
	if reply.Error == nil || reply.Error.Message != "PATTERN_NOT_FOUND" {
		t.Errorf("reply.Error = %+v, want PATTERN_NOT_FOUND", reply.Error)
	}
	if len(record.handlers()) != 0 {
		t.Errorf("handlers %v ran for an unrouted subject", record.handlers())
	}
}

// TestGuardsApplyToMessageHandlers is what makes the tags uniform: the same
// guard that protects an HTTP route must protect a message handler.
func TestGuardsApplyToMessageHandlers(t *testing.T) {
	type controller struct {
		Delete func(*gin.Context) `transport:"memory" pattern:"user_delete" guard:"Admin"`
	}

	handlerRan := false

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	app.AddGuard("Admin", func(args []string) gin.HandlerFunc {
		return func(c *gin.Context) {
			// The guard reads the envelope header, exactly as an HTTP guard reads
			// a request header.
			if c.GetHeader("Authorization") != "Bearer admin" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"success": false,
					"error":   gin.H{"code": 403, "message": "FORBIDDEN"},
				})
				return
			}
			c.Next()
		}
	})

	transport := NewMemory()
	server, err := Setup(app, Config{Transport: transport})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	app.RegisterControllers(&controller{
		Delete: func(c *gin.Context) {
			handlerRan = true
			c.JSON(http.StatusOK, gin.H{"success": true})
		},
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}

	t.Run("blocked without the header", func(t *testing.T) {
		handlerRan = false
		reply := dispatch(t, transport, "user_delete", nil)

		if handlerRan {
			t.Error("the handler ran even though the guard aborted the message")
		}
		if reply.Status != http.StatusForbidden {
			t.Errorf("reply status = %d, want 403", reply.Status)
		}
	})

	t.Run("allowed with the header", func(t *testing.T) {
		handlerRan = false
		env, _ := NewEnvelope("user_delete", nil)
		env.WithHeader("Authorization", "Bearer admin")

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		reply, err := transport.Dispatch(ctx, env)
		if err != nil {
			t.Fatalf("Dispatch returned %v", err)
		}

		if !handlerRan {
			t.Error("the handler did not run despite a valid guard header")
		}
		if reply.Status != http.StatusOK {
			t.Errorf("reply status = %d, want 200", reply.Status)
		}
	})
}

// TestEnvelopeHeadersCannotSpoofTheClientIP closes an injection path: envelope
// headers come from a remote publisher, so a forwarded-for header must not reach
// the handler and change what ClientIP() reports.
func TestEnvelopeHeadersCannotSpoofTheClientIP(t *testing.T) {
	type controller struct {
		Who func(*gin.Context) `transport:"memory" pattern:"whoami"`
	}

	var seenIP, seenForwarded, seenCustom string

	_, transport, _ := newServer(t, &controller{
		Who: func(c *gin.Context) {
			seenIP = c.ClientIP()
			seenForwarded = c.GetHeader("X-Forwarded-For")
			seenCustom = c.GetHeader("X-Tenant")
			c.JSON(http.StatusOK, gin.H{"success": true})
		},
	})

	env, _ := NewEnvelope("whoami", nil)
	env.WithHeader("X-Forwarded-For", "1.2.3.4")
	env.WithHeader("X-Tenant", "acme")
	// A publisher must not be able to override the body framing either.
	env.WithHeader("Content-Length", "999999")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := transport.Dispatch(ctx, env); err != nil {
		t.Fatalf("Dispatch returned %v", err)
	}

	if seenForwarded != "" {
		t.Errorf("X-Forwarded-For reached the handler as %q, want it dropped", seenForwarded)
	}
	if seenIP == "1.2.3.4" {
		t.Errorf("ClientIP() = %q, spoofed by an envelope header", seenIP)
	}
	// A legitimate application header must still get through.
	if seenCustom != "acme" {
		t.Errorf("X-Tenant = %q, want \"acme\" — application headers must survive", seenCustom)
	}
}

// TestHandlerPanicBecomesAReplyNotACrash matters more for a consumer than for an
// HTTP server: a panic that escapes takes down the process and stops consuming
// every subject, not just the failing one.
func TestHandlerPanicBecomesAReplyNotACrash(t *testing.T) {
	type controller struct {
		Boom func(*gin.Context) `transport:"memory" pattern:"boom"`
		Fine func(*gin.Context) `transport:"memory" pattern:"fine"`
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := NewMemory()

	// The internal engine needs its own recovery: it is a separate engine from
	// the HTTP one, so the app's recovery middleware does not cover it.
	server, err := Setup(app, Config{
		Transport:  transport,
		Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
	})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	app.RegisterControllers(&controller{
		Boom: func(c *gin.Context) { panic("handler exploded") },
		Fine: func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) },
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}

	reply := dispatch(t, transport, "boom", nil)
	if reply.Status != http.StatusInternalServerError {
		t.Errorf("reply status = %d, want 500", reply.Status)
	}
	if strings.Contains(string(reply.Data), "handler exploded") {
		t.Error("the panic value leaked into the reply")
	}

	// The consumer must still be serving other subjects.
	if reply := dispatch(t, transport, "fine", nil); reply.Status != http.StatusOK {
		t.Errorf("after a panic, \"fine\" = %d, want 200 — the consumer died", reply.Status)
	}
}

func TestSetupRejectsBadConfig(t *testing.T) {
	t.Run("no transport", func(t *testing.T) {
		app := nika.NewApp(nika.Config{Mode: gin.TestMode})
		if _, err := Setup(app, Config{}); err == nil {
			t.Error("Setup with no transport returned nil, want an error")
		}
	})

	t.Run("nil app", func(t *testing.T) {
		if _, err := Setup(nil, Config{Transport: NewMemory()}); err == nil {
			t.Error("Setup with a nil app returned nil, want an error")
		}
	})

	t.Run("duplicate transport", func(t *testing.T) {
		app := nika.NewApp(nika.Config{Mode: gin.TestMode})
		_, err := Setup(app, Config{
			Transport:  NewMemory(),
			Transports: []Listener{NewMemory()},
		})
		if err == nil {
			t.Error("Setup with the same transport twice returned nil, want an error")
		}
	})
}

func TestRegisterControllersRejectsBadTags(t *testing.T) {
	newSetup := func(t *testing.T) (*Server, *nika.App) {
		t.Helper()
		app := nika.NewApp(nika.Config{Mode: gin.TestMode})
		server, err := Setup(app, Config{Transport: NewMemory()})
		if err != nil {
			t.Fatalf("Setup returned %v", err)
		}
		return server, app
	}

	t.Run("transport without a pattern", func(t *testing.T) {
		type controller struct {
			Create func(*gin.Context) `transport:"memory"`
		}
		server, _ := newSetup(t)

		err := server.RegisterControllers(&controller{Create: func(*gin.Context) {}})
		if err == nil || !strings.Contains(err.Error(), "no pattern tag") {
			t.Errorf("error = %v, want it to name the missing pattern tag", err)
		}
	})

	t.Run("unconfigured transport", func(t *testing.T) {
		type controller struct {
			Create func(*gin.Context) `transport:"kafka" pattern:"user_created"`
		}
		server, _ := newSetup(t)

		err := server.RegisterControllers(&controller{Create: func(*gin.Context) {}})
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Errorf("error = %v, want it to say the transport is not configured", err)
		}
	})

	t.Run("nil handler", func(t *testing.T) {
		type controller struct {
			Create func(*gin.Context) `transport:"memory" pattern:"user_created"`
		}
		server, _ := newSetup(t)

		err := server.RegisterControllers(&controller{})
		if err == nil || !strings.Contains(err.Error(), "is nil") {
			t.Errorf("error = %v, want it to report the nil handler", err)
		}
	})

	t.Run("duplicate pattern", func(t *testing.T) {
		type controller struct {
			A func(*gin.Context) `transport:"memory" pattern:"user_created"`
			B func(*gin.Context) `transport:"memory" pattern:"user_created"`
		}
		server, _ := newSetup(t)

		noop := func(*gin.Context) {}
		err := server.RegisterControllers(&controller{A: noop, B: noop})
		if err == nil || !strings.Contains(err.Error(), "already handled") {
			t.Errorf("error = %v, want a duplicate-pattern error", err)
		}
	})
}

// TestSetupBeforeOrAfterLoadModule pins the ordering guarantee: wiring the
// microservice layer must not have to happen at one specific point in bootstrap,
// because getting it wrong would silently serve nothing.
func TestSetupBeforeOrAfterLoadModule(t *testing.T) {
	t.Run("controllers registered after Setup", func(t *testing.T) {
		record := &calls{}
		_, transport, _ := newServer(t, newUserController(record))

		if reply := dispatch(t, transport, "users", nil); reply.Status != http.StatusOK {
			t.Errorf("reply status = %d, want 200", reply.Status)
		}
	})

	t.Run("controllers registered before Setup", func(t *testing.T) {
		record := &calls{}
		app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})

		// Registered first — the observer replay must still pick it up.
		app.RegisterControllers(newUserController(record))

		transport := NewMemory()
		server, err := Setup(app, Config{Transport: transport})
		if err != nil {
			t.Fatalf("Setup returned %v", err)
		}
		t.Cleanup(func() { _ = server.Stop(context.Background()) })

		if err := app.Start(context.Background()); err != nil {
			t.Fatalf("Start returned %v", err)
		}

		if reply := dispatch(t, transport, "users", nil); reply.Status != http.StatusOK {
			t.Errorf("reply status = %d, want 200 — the replayed controller was missed", reply.Status)
		}
	})
}

// --- client / transport round trip ---------------------------------------

func TestClientRequestReplyThroughTheQueue(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	client := NewClient(transport, WithTimeout(3*time.Second))

	var out struct {
		Success bool `json:"success"`
		Data    struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}

	if err := client.Send(context.Background(), "user_created", CreateUserDto{Name: "Ada"}, &out); err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if !out.Success || out.Data.Name != "Ada" {
		t.Errorf("decoded reply = %+v, want success with name Ada", out)
	}
}

func TestClientEmitIsFireAndForget(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	client := NewClient(transport)
	if err := client.Emit(context.Background(), "user_created", CreateUserDto{Name: "Grace"}); err != nil {
		t.Fatalf("Emit returned %v", err)
	}

	// Emit does not wait, so poll for the side effect rather than sleeping a
	// fixed amount — the latter is how a suite becomes flaky.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(record.handlers()) > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("the emitted message never reached a handler")
}

// TestClientSurfacesAHandlerErrorAsEnvelopeError is what lets a caller decide
// whether retrying is safe: "the service said no" must be distinguishable from
// "the message never arrived".
func TestClientSurfacesAHandlerErrorAsEnvelopeError(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	client := NewClient(transport, WithTimeout(3*time.Second))
	err := client.Send(context.Background(), "unknown_subject", nil, nil)

	if err == nil {
		t.Fatal("Send to an unrouted subject returned nil, want an error")
	}

	var envErr *EnvelopeError
	if !errors.As(err, &envErr) {
		t.Fatalf("error is %T, want *EnvelopeError", err)
	}
	if envErr.Code != http.StatusNotFound {
		t.Errorf("EnvelopeError.Code = %d, want 404", envErr.Code)
	}
}

func TestClientRejectsAWildcardSubject(t *testing.T) {
	client := NewClient(NewMemory())

	// Sending to a wildcard is ambiguous about which service should answer, so it
	// must fail at the call site rather than fan out unpredictably.
	err := client.Emit(context.Background(), "user_*", nil)
	if err == nil {
		t.Fatal("Emit to a wildcard returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("error = %v, want it to explain the wildcard restriction", err)
	}
}

func TestRequestTimesOutWhenNobodyAnswers(t *testing.T) {
	transport := NewMemory()
	t.Cleanup(func() { _ = transport.Close() })

	// No Listen call, so nothing consumes; the buffered queue accepts the message
	// and the reply never comes.
	env, _ := NewEnvelope("user_created", nil)

	start := time.Now()
	_, err := transport.Request(context.Background(), env, 100*time.Millisecond)

	if !errors.Is(err, ErrTimeout) {
		t.Errorf("Request returned %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Request took %s to time out, want ~100ms", elapsed)
	}
}

func TestClosedTransportRejectsWork(t *testing.T) {
	transport := NewMemory()
	if err := transport.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
	// Close must be idempotent: a shutdown hook and a defer both calling it is
	// normal, and a double close of a channel would panic.
	if err := transport.Close(); err != nil {
		t.Errorf("the second Close returned %v, want nil", err)
	}

	env, _ := NewEnvelope("user_created", nil)

	if err := transport.Publish(context.Background(), env); !errors.Is(err, ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if _, err := transport.Request(context.Background(), env, time.Second); !errors.Is(err, ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
}

func TestServerStopIsIdempotent(t *testing.T) {
	record := &calls{}
	server, _, _ := newServer(t, newUserController(record))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := server.Stop(ctx); err != nil {
		t.Errorf("Stop returned %v", err)
	}
	if err := server.Stop(ctx); err != nil {
		t.Errorf("the second Stop returned %v, want nil", err)
	}
}

func TestConcurrentDispatchIsRaceFree(t *testing.T) {
	record := &calls{}
	_, transport, _ := newServer(t, newUserController(record))

	client := NewClient(transport, WithTimeout(5*time.Second))

	var wg sync.WaitGroup
	subjects := []string{"user_created", "user_1", "users", "user_2"}

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = client.Emit(context.Background(), subjects[i%len(subjects)], CreateUserDto{Name: "x"})
		}(i)
	}
	wg.Wait()
}

func TestServerRoutesListsEveryHandler(t *testing.T) {
	record := &calls{}
	server, _, _ := newServer(t, newUserController(record))

	routes := server.Routes()
	if len(routes) != 3 {
		t.Fatalf("Routes() has %d entries, want 3: %v", len(routes), routes)
	}

	// The route must name its declaring handler, so a startup log or a health
	// page tells an operator what is actually wired.
	for _, r := range routes {
		if r.Controller != "UserController" {
			t.Errorf("route %s has Controller %q, want \"UserController\"", r.Pattern, r.Controller)
		}
		if r.Field == "" {
			t.Errorf("route %s has no Field", r.Pattern)
		}
	}
}
