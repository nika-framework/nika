package microservice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// --- Named ----------------------------------------------------------------

// TestNamedOverridesTheTransportName is the point of the wrapper: the tag then
// addresses a service, not a broker type.
func TestNamedOverridesTheTransportName(t *testing.T) {
	underlying := NewMemory()
	named := Named("users-svc", underlying)

	if got := named.Name(); got != "users-svc" {
		t.Errorf("Name() = %q, want \"users-svc\"", got)
	}
	// The broker type must still be recoverable, because a log line saying
	// "users-svc failed" is less useful than "users-svc (memory) failed".
	if got := TransportName(named); got != TransportMemory {
		t.Errorf("TransportName() = %q, want %q", got, TransportMemory)
	}
}

// TestNamedPreservesThePublishHalf catches the mistake of wrapping a full
// Transport in a Listener-only wrapper, which would make the renamed transport
// unusable as a client.
func TestNamedPreservesThePublishHalf(t *testing.T) {
	named := Named("users-svc", NewMemory())

	publisher, ok := named.(Publisher)
	if !ok {
		t.Fatalf("Named returned %T, which is not a Publisher — a renamed transport cannot publish", named)
	}
	if got := publisher.Name(); got != "users-svc" {
		t.Errorf("Publisher.Name() = %q, want \"users-svc\"", got)
	}
	if _, ok := named.(Transport); !ok {
		t.Error("Named did not preserve the full Transport interface")
	}
}

func TestNamedRejectsBadInput(t *testing.T) {
	t.Run("nil transport", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Named(nil) did not panic")
			}
		}()
		Named("x", nil)
	})

	t.Run("empty name", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Named with an empty name did not panic")
			}
		}()
		Named("   ", NewMemory())
	})
}

// TestNamedTransportRoutesByServiceName is the end-to-end proof: a handler tagged
// with the *service* name is reached, and the broker type is not a valid tag any
// more once the transport is renamed.
func TestNamedTransportRoutesByServiceName(t *testing.T) {
	type controller struct {
		ListUser func(*gin.Context) `transport:"users-svc" pattern:"users"`
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	underlying := NewMemory()

	server, err := Setup(app, Config{Transport: Named("users-svc", underlying)})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	app.RegisterControllers(&controller{
		ListUser: func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": []string{"ada"}})
		},
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}

	if router := server.Router("users-svc"); router == nil {
		t.Fatal("no router registered under the service name")
	}
	if server.Router(TransportMemory) != nil {
		t.Error("a router is still registered under the broker type; the rename did not take")
	}

	reply := dispatch(t, underlying, "users", nil)
	if reply.Status != http.StatusOK {
		t.Errorf("reply status = %d, want 200", reply.Status)
	}
}

// TestUnknownServiceNameNamesTheConfiguredOnes: a tag that does not match any
// configured service must fail at startup with the alternatives listed, since a
// typo here would otherwise mean a handler that silently never receives anything.
func TestUnknownServiceNameNamesTheConfiguredOnes(t *testing.T) {
	type controller struct {
		ListUser func(*gin.Context) `transport:"user-svc" pattern:"users"` // typo: missing 's'
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode})
	server, err := Setup(app, Config{Transport: Named("users-svc", NewMemory())})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}

	err = server.RegisterControllers(&controller{ListUser: func(*gin.Context) {}})
	if err == nil {
		t.Fatal("registering a controller with an unknown service name returned nil")
	}
	if !strings.Contains(err.Error(), "users-svc") {
		t.Errorf("error = %v, want it to list the configured service names", err)
	}
}

// --- Clients registry -----------------------------------------------------

func TestClientsRegistry(t *testing.T) {
	registry := NewClients()
	transport := NewMemory()
	t.Cleanup(func() { _ = transport.Close() })

	if err := registry.Register("users-svc", NewClient(transport)); err != nil {
		t.Fatalf("Register returned %v", err)
	}

	if _, err := registry.Client("users-svc"); err != nil {
		t.Errorf("Client(\"users-svc\") returned %v", err)
	}

	t.Run("unknown service names the configured ones", func(t *testing.T) {
		_, err := registry.Client("orders-svc")
		if err == nil {
			t.Fatal("Client of an unknown service returned nil")
		}
		if !strings.Contains(err.Error(), "users-svc") {
			t.Errorf("error = %v, want it to list \"users-svc\"", err)
		}
	})

	t.Run("duplicate registration is rejected", func(t *testing.T) {
		if err := registry.Register("users-svc", NewClient(NewMemory())); err == nil {
			t.Error("registering a duplicate service name returned nil")
		}
	})

	t.Run("empty name is rejected", func(t *testing.T) {
		if err := registry.Register("", NewClient(NewMemory())); err == nil {
			t.Error("registering an empty service name returned nil")
		}
	})

	t.Run("nil client is rejected", func(t *testing.T) {
		if err := registry.Register("nil-svc", nil); err == nil {
			t.Error("registering a nil client returned nil")
		}
	})
}

// apiFixture boots a service plus an HTTP endpoint that calls it, which is the
// shape the framework is being asked to support: an API handler fronting a
// microservice.
func apiFixture(t *testing.T) *nika.App {
	t.Helper()

	type userService struct {
		ListUser func(*gin.Context) `transport:"users-svc" pattern:"users"`
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := NewMemory()

	server, err := Setup(app, Config{Transport: Named("users-svc", transport)})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	// The client registry must be installed before any route is registered, or
	// gin will have frozen those routes' middleware chains already.
	if _, err := SetupClients(app, map[string]Publisher{"users-svc": transport}); err != nil {
		t.Fatalf("SetupClients returned %v", err)
	}

	app.RegisterControllers(&userService{
		ListUser: func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data":    []gin.H{{"id": "1", "name": "Ada"}},
				"total":   1,
			})
		},
	})

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}
	return app
}

type listUsersResponse struct {
	Success bool `json:"success"`
	Data    []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"data"`
	Total int `json:"total"`
}

// TestCallFromAnHTTPHandler is the requested capability: an HTTP endpoint calls a
// microservice and gets a typed reply back.
func TestCallFromAnHTTPHandler(t *testing.T) {
	app := apiFixture(t)

	app.GET("/users", func(c *gin.Context) {
		users, err := Call[listUsersResponse](c, "users-svc", "users", nil)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"success": false,
				"error":   gin.H{"code": 502, "message": "LIST_FAILED"},
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "count": users.Total, "first": users.Data[0].Name})
	})

	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/users", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("GET /users = %d, want 200\n  body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"first":"Ada"`) {
		t.Errorf("body = %s, want the decoded reply", res.Body.String())
	}
}

func TestCallReportsAnUnknownService(t *testing.T) {
	app := apiFixture(t)

	app.GET("/broken", func(c *gin.Context) {
		_, err := Call[listUsersResponse](c, "orders-svc", "orders", nil)
		if err == nil {
			c.String(http.StatusOK, "unexpected success")
			return
		}
		c.String(http.StatusBadGateway, err.Error())
	})

	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/broken", nil))

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.Code)
	}
	if !strings.Contains(res.Body.String(), "users-svc") {
		t.Errorf("error = %s, want it to list the configured services", res.Body.String())
	}
}

// TestCallSurfacesAHandlerError keeps the distinction the whole error model rests
// on: a remote rejection must reach the API handler as an *EnvelopeError so it can
// map it to a status rather than blanket-500 everything.
func TestCallSurfacesAHandlerError(t *testing.T) {
	app := apiFixture(t)

	app.GET("/missing", func(c *gin.Context) {
		_, err := Call[listUsersResponse](c, "users-svc", "not_a_pattern", nil)

		var envErr *EnvelopeError
		if errors.As(err, &envErr) {
			c.JSON(http.StatusBadGateway, gin.H{"code": envErr.Code, "message": envErr.Message})
			return
		}
		c.String(http.StatusInternalServerError, "wrong error type: %v", err)
	})

	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502\n  body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "PATTERN_NOT_FOUND") {
		t.Errorf("body = %s, want PATTERN_NOT_FOUND", res.Body.String())
	}
}

// TestCallWithoutTheMiddlewareExplainsItself: the failure mode of registering
// routes before SetupClients is a nil registry, so the error has to say what to do
// rather than being a nil dereference.
func TestCallWithoutTheMiddlewareExplainsItself(t *testing.T) {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode})

	app.GET("/users", func(c *gin.Context) {
		_, err := Call[listUsersResponse](c, "users-svc", "users", nil)
		if err == nil {
			c.String(http.StatusOK, "unexpected success")
			return
		}
		c.String(http.StatusInternalServerError, err.Error())
	})

	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/users", nil))

	body := res.Body.String()
	if !strings.Contains(body, "SetupClients") {
		t.Errorf("error = %q, want it to name SetupClients", body)
	}
	if !strings.Contains(body, "before LoadModule") {
		t.Errorf("error = %q, want it to explain the ordering requirement", body)
	}
}

// TestClientsInjectedByDI is the ordering-hazard-free alternative, and the one the
// documentation recommends.
func TestClientsInjectedByDI(t *testing.T) {
	app := apiFixture(t)

	registry, ok := nika.Resolve[*Clients](app)
	if !ok {
		t.Fatal("*Clients is not registered in the DI container")
	}

	var reply listUsersResponse
	if err := registry.Send(context.Background(), "users-svc", "users", nil, &reply); err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if reply.Total != 1 || len(reply.Data) != 1 {
		t.Errorf("reply = %+v, want one user", reply)
	}
}

func TestCallEmitFromAnHTTPHandler(t *testing.T) {
	app := apiFixture(t)

	app.POST("/notify", func(c *gin.Context) {
		if err := CallEmit(c, "users-svc", "users", nil); err != nil {
			c.String(http.StatusBadGateway, err.Error())
			return
		}
		c.Status(http.StatusAccepted)
	})

	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/notify", nil))

	if res.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202\n  body: %s", res.Code, res.Body.String())
	}
}

// TestCallInheritsTheRequestContext matters for load shedding: a client that hangs
// up must not leave the downstream call running to completion.
func TestCallInheritsTheRequestContext(t *testing.T) {
	type slowService struct {
		Slow func(*gin.Context) `transport:"slow-svc" pattern:"slow"`
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := NewMemory()

	server, err := Setup(app, Config{Transport: Named("slow-svc", transport)})
	if err != nil {
		t.Fatalf("Setup returned %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	if _, err := SetupClients(app, map[string]Publisher{"slow-svc": transport}); err != nil {
		t.Fatalf("SetupClients returned %v", err)
	}

	released := make(chan struct{})
	app.RegisterControllers(&slowService{
		Slow: func(c *gin.Context) {
			<-released
			c.JSON(http.StatusOK, gin.H{"success": true})
		},
	})

	var callErr error
	app.GET("/slow", func(c *gin.Context) {
		_, callErr = Call[listUsersResponse](c, "slow-svc", "slow", nil)
		c.Status(http.StatusOK)
	})

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start returned %v", err)
	}

	// Cancel the inbound request while the downstream handler is still blocked.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)

	close(released)

	if callErr == nil {
		t.Error("the downstream call succeeded despite the request being cancelled")
	}
}

func TestSetupClientsRejectsBadInput(t *testing.T) {
	t.Run("nil app", func(t *testing.T) {
		if _, err := SetupClients(nil, map[string]Publisher{"a": NewMemory()}); err == nil {
			t.Error("SetupClients with a nil app returned nil")
		}
	})

	t.Run("no publishers", func(t *testing.T) {
		app := nika.NewApp(nika.Config{Mode: gin.TestMode})
		if _, err := SetupClients(app, nil); err == nil {
			t.Error("SetupClients with no publishers returned nil")
		}
	})

	t.Run("nil publisher", func(t *testing.T) {
		app := nika.NewApp(nika.Config{Mode: gin.TestMode})
		if _, err := SetupClients(app, map[string]Publisher{"a": nil}); err == nil {
			t.Error("SetupClients with a nil publisher returned nil")
		}
	})
}

func TestClientsCloseIsIdempotent(t *testing.T) {
	registry := NewClients()
	if err := registry.Register("a", NewClient(NewMemory())); err != nil {
		t.Fatalf("Register returned %v", err)
	}

	if err := registry.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Errorf("the second Close returned %v, want nil", err)
	}
}
