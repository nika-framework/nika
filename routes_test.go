package nika

import (
	"net/http"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseRouteTag(t *testing.T) {
	tests := []struct {
		name       string
		tag        string
		wantMethod string
		wantPath   string
		wantErr    bool
	}{
		{name: "simple get", tag: "GET:/users", wantMethod: "GET", wantPath: "/users"},
		{name: "lowercase method is normalised", tag: "get:/users", wantMethod: "GET", wantPath: "/users"},
		{name: "surrounding spaces are trimmed", tag: " post : /users ", wantMethod: "POST", wantPath: "/users"},
		{name: "path with params", tag: "GET:/users/:id/posts", wantMethod: "GET", wantPath: "/users/:id/posts"},
		{name: "path with a colon keeps it", tag: "GET:/users/:id", wantMethod: "GET", wantPath: "/users/:id"},
		{name: "root path", tag: "GET:/", wantMethod: "GET", wantPath: "/"},
		{name: "no separator", tag: "GET", wantErr: true},
		{name: "empty method", tag: ":/users", wantErr: true},
		{name: "empty path", tag: "GET:", wantErr: true},
		{name: "path without a leading slash", tag: "GET:users", wantErr: true},
		{name: "path containing a space", tag: "GET:/user list", wantErr: true},
		{name: "path containing a newline", tag: "GET:/users\n/admin", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method, path, err := parseRouteTag(test.tag)

			if test.wantErr {
				if err == nil {
					t.Fatalf("parseRouteTag(%q) = (%q, %q), want an error", test.tag, method, path)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteTag(%q) returned an unexpected error: %v", test.tag, err)
			}
			if method != test.wantMethod || path != test.wantPath {
				t.Errorf("parseRouteTag(%q) = (%q, %q), want (%q, %q)",
					test.tag, method, path, test.wantMethod, test.wantPath)
			}
		})
	}
}

func TestRegisterControllersRegistersEveryMethod(t *testing.T) {
	type controller struct {
		Get     func(*gin.Context) `route:"GET:/thing"`
		Post    func(*gin.Context) `route:"POST:/thing"`
		Put     func(*gin.Context) `route:"PUT:/thing"`
		Patch   func(*gin.Context) `route:"PATCH:/thing"`
		Delete  func(*gin.Context) `route:"DELETE:/thing"`
		Head    func(*gin.Context) `route:"HEAD:/thing"`
		Options func(*gin.Context) `route:"OPTIONS:/thing"`

		// No route tag: must be ignored rather than treated as a broken route.
		Helper func(*gin.Context)
		Name   string
	}

	app := newTestApp()
	handler := okHandler("ok")
	app.RegisterControllers(&controller{
		Get: handler, Post: handler, Put: handler, Patch: handler,
		Delete: handler, Head: handler, Options: handler, Helper: handler,
		Name: "ignored",
	})

	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if got := do(app, method, "/thing").Code; got != http.StatusOK {
			t.Errorf("%s /thing = %d, want 200", method, got)
		}
	}

	routes := app.Routes()
	sort.Strings(routes)
	if len(routes) != 7 {
		t.Errorf("Routes() has %d entries, want 7: %v", len(routes), routes)
	}
}

func TestRegisterControllersAcceptsGinHandlerFunc(t *testing.T) {
	// A handler declared as gin.HandlerFunc rather than func(*gin.Context) is
	// the same signature to a human but a different reflect.Type; the previous
	// unchecked type assertion panicked on it.
	type controller struct {
		List gin.HandlerFunc `route:"GET:/items"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{
		List: func(c *gin.Context) { c.String(http.StatusOK, "items") },
	})

	res := do(app, "GET", "/items")
	if res.Code != http.StatusOK || res.Body.String() != "items" {
		t.Errorf("GET /items = %d %q, want 200 \"items\"", res.Code, res.Body.String())
	}
}

func TestRegisterControllersRejectsBadDeclarations(t *testing.T) {
	t.Run("nil handler", func(t *testing.T) {
		type controller struct {
			List func(*gin.Context) `route:"GET:/items"`
		}
		app := newTestApp()

		// An unassigned handler used to reach gin and fail there with a message
		// that named neither the controller nor the field.
		defer expectPanic(t, "route handler is nil")
		app.RegisterControllers(&controller{})
	})

	t.Run("wrong signature", func(t *testing.T) {
		type controller struct {
			List func(string) `route:"GET:/items"`
		}
		app := newTestApp()

		defer expectPanic(t, "func(*gin.Context)")
		app.RegisterControllers(&controller{List: func(string) {}})
	})

	t.Run("not a function", func(t *testing.T) {
		type controller struct {
			List string `route:"GET:/items"`
		}
		app := newTestApp()

		defer expectPanic(t, "must be a func")
		app.RegisterControllers(&controller{List: "nope"})
	})

	t.Run("unsupported method", func(t *testing.T) {
		type controller struct {
			List func(*gin.Context) `route:"FETCH:/items"`
		}
		app := newTestApp()

		defer expectPanic(t, "unsupported method")
		app.RegisterControllers(&controller{List: okHandler("x")})
	})

	t.Run("unregistered guard", func(t *testing.T) {
		type controller struct {
			List func(*gin.Context) `route:"GET:/items" guard:"Auth"`
		}
		app := newTestApp()

		defer expectPanic(t, "is not registered")
		app.RegisterControllers(&controller{List: okHandler("x")})
	})

	t.Run("nil controller", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "nil controller")
		app.RegisterControllers(nil)
	})

	t.Run("duplicate route names the second declaration", func(t *testing.T) {
		type first struct {
			List func(*gin.Context) `route:"GET:/items"`
		}
		type second struct {
			Index func(*gin.Context) `route:"GET:/items"`
		}
		app := newTestApp()
		app.RegisterControllers(&first{List: okHandler("a")})

		defer expectPanic(t, "duplicate route GET /items")
		app.RegisterControllers(&second{Index: okHandler("b")})
	})
}

func TestGuardChainRunsBeforeHandlerInOrder(t *testing.T) {
	var order []string

	app := newTestApp()
	app.AddGuard("First", func([]string) gin.HandlerFunc {
		return func(c *gin.Context) {
			order = append(order, "first")
			c.Next()
		}
	})
	app.AddGuard("Second", func([]string) gin.HandlerFunc {
		return func(c *gin.Context) {
			order = append(order, "second")
			c.Next()
		}
	})

	type controller struct {
		List func(*gin.Context) `route:"GET:/items" guard:"First Second"`
	}
	app.RegisterControllers(&controller{
		List: func(c *gin.Context) {
			order = append(order, "handler")
			c.Status(http.StatusOK)
		},
	})

	do(app, "GET", "/items")

	want := []string{"first", "second", "handler"}
	if len(order) != len(want) {
		t.Fatalf("chain ran %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("chain ran %v, want %v", order, want)
		}
	}
}

// TestGuardCanBlockTheHandler is the assertion that matters for authorization: a
// guard that aborts must stop the handler from running at all.
func TestGuardCanBlockTheHandler(t *testing.T) {
	handlerRan := false

	app := newTestApp()
	app.AddGuard("Deny", func([]string) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.AbortWithStatus(http.StatusForbidden)
		}
	})

	type controller struct {
		Secret func(*gin.Context) `route:"GET:/secret" guard:"Deny"`
	}
	app.RegisterControllers(&controller{
		Secret: func(c *gin.Context) {
			handlerRan = true
			c.String(http.StatusOK, "leaked")
		},
	})

	res := do(app, "GET", "/secret")

	if handlerRan {
		t.Error("the handler ran even though the guard aborted the request")
	}
	if res.Code != http.StatusForbidden {
		t.Errorf("GET /secret = %d, want 403", res.Code)
	}
	if res.Body.String() == "leaked" {
		t.Error("the protected body was written despite the guard")
	}
}

func TestRegisterControllersSkipsMessageOnlyFields(t *testing.T) {
	// A field tagged only for a transport belongs to the microservice layer; the
	// HTTP router must not try to mount it.
	type controller struct {
		OnCreate func(*gin.Context) `transport:"redis" pattern:"user_created"`
		List     func(*gin.Context) `route:"GET:/users"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{
		OnCreate: okHandler("message"),
		List:     okHandler("http"),
	})

	if routes := app.Routes(); len(routes) != 1 || routes[0] != "GET /users" {
		t.Errorf("Routes() = %v, want [\"GET /users\"]", routes)
	}
}

func TestGroupReturnsTheGroup(t *testing.T) {
	// The previous implementation created the group, discarded it and returned
	// the engine, so every handler registered on the result silently lost the
	// prefix.
	app := newTestApp()

	group := app.Group("/api/v1")
	group.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	if res := do(app, "GET", "/api/v1/ping"); res.Code != http.StatusOK {
		t.Errorf("GET /api/v1/ping = %d, want 200 (the group prefix was dropped)", res.Code)
	}
	if res := do(app, "GET", "/ping"); res.Code == http.StatusOK {
		t.Error("GET /ping succeeded, so the route was registered without its group prefix")
	}
}

func TestExpectedRoutesAreReported(t *testing.T) {
	type controller struct {
		List   func(*gin.Context) `route:"GET:/users"`
		Create func(*gin.Context) `route:"POST:/users"`
	}

	app := newTestApp()
	app.RegisterControllers(&controller{List: okHandler("a"), Create: okHandler("b")})

	routes := app.Routes()
	sort.Strings(routes)

	want := []string{"GET /users", "POST /users"}
	if len(routes) != len(want) {
		t.Fatalf("Routes() = %v, want %v", routes, want)
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Fatalf("Routes() = %v, want %v", routes, want)
		}
	}
}
