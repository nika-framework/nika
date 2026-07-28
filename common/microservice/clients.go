package microservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// clientsContextKey is the gin context key holding the registry.
const clientsContextKey = "nika.microservice.clients"

// Clients is a registry of named microservice clients, so an HTTP handler can
// call a service by name instead of holding a client per dependency.
//
//	users, err := microservice.Call[ListUsersResponse](c, "users-svc", "users", dto)
//
// The name is the service name, matching what Named gave the transport and what
// the handler's `transport:"..."` tag says on the other side.
type Clients struct {
	mu     sync.RWMutex
	byName map[string]*Client
}

// NewClients returns an empty registry.
func NewClients() *Clients {
	return &Clients{byName: make(map[string]*Client)}
}

// Register adds a client under a service name.
func (cs *Clients) Register(name string, client *Client) error {
	if name == "" {
		return errors.New("microservice: a client needs a service name")
	}
	if client == nil {
		return fmt.Errorf("microservice: client for %q is nil", name)
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, exists := cs.byName[name]; exists {
		return fmt.Errorf("microservice: a client is already registered for %q", name)
	}
	cs.byName[name] = client
	return nil
}

// Client returns the client registered for a service name.
func (cs *Clients) Client(service string) (*Client, error) {
	cs.mu.RLock()
	client, ok := cs.byName[service]
	cs.mu.RUnlock()

	if ok {
		return client, nil
	}
	// Name the configured services in the error: a typo in a service name is the
	// most likely cause, and listing the alternatives makes it self-diagnosing.
	return nil, fmt.Errorf(
		"microservice: no client registered for service %q (configured: %v)",
		service, cs.Names(),
	)
}

// Names returns every registered service name, sorted.
func (cs *Clients) Names() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	names := make([]string, 0, len(cs.byName))
	for name := range cs.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Send performs a request/reply against a named service and decodes the reply
// into out.
func (cs *Clients) Send(ctx context.Context, service, pattern string, payload, out any) error {
	client, err := cs.Client(service)
	if err != nil {
		return err
	}
	return client.Send(ctx, pattern, payload, out)
}

// Request performs a request/reply and returns the raw reply envelope.
func (cs *Clients) Request(ctx context.Context, service, pattern string, payload any) (*Envelope, error) {
	client, err := cs.Client(service)
	if err != nil {
		return nil, err
	}
	return client.Request(ctx, pattern, payload)
}

// Emit publishes to a named service without waiting for a reply.
func (cs *Clients) Emit(ctx context.Context, service, pattern string, payload any) error {
	client, err := cs.Client(service)
	if err != nil {
		return err
	}
	return client.Emit(ctx, pattern, payload)
}

// Close closes every registered client, returning the first failure but always
// attempting all of them so no connection is left open.
func (cs *Clients) Close() error {
	cs.mu.Lock()
	clients := make([]*Client, 0, len(cs.byName))
	for _, client := range cs.byName {
		clients = append(clients, client)
	}
	cs.byName = make(map[string]*Client)
	cs.mu.Unlock()

	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SetupClients builds a client per named publisher, registers the registry in the
// DI container, exposes it on the gin context, and closes everything on shutdown.
//
//	microservice.SetupClients(app, map[string]microservice.Publisher{
//	    "users-svc":  redismq.MustNew(redismq.Options{URL: redisURL}),
//	    "orders-svc": natsmq.MustNew(natsmq.Options{URL: natsURL}),
//	})
//
// Call it BEFORE LoadModule. Gin freezes a route's middleware chain when the
// route is registered, so the middleware that publishes the registry onto the
// context only reaches routes registered after this call. A route registered
// earlier would see a nil registry — SetupClients logs a warning when it detects
// that, rather than leaving you to find it in production.
//
// Injecting *Clients into a controller constructor has no such ordering hazard
// and is the better choice where you control the constructor; see the package
// documentation.
func SetupClients(app *nika.App, publishers map[string]Publisher, opts ...ClientOption) (*Clients, error) {
	if app == nil {
		return nil, errors.New("microservice: app is required")
	}
	if len(publishers) == 0 {
		return nil, errors.New("microservice: SetupClients needs at least one publisher")
	}

	registry := NewClients()
	for name, publisher := range publishers {
		if publisher == nil {
			return nil, fmt.Errorf("microservice: publisher for %q is nil", name)
		}
		if err := registry.Register(name, NewClient(publisher, opts...)); err != nil {
			return nil, err
		}
	}

	if existing := app.Routes(); len(existing) > 0 {
		nika.Logger().Warn(
			"microservice: SetupClients was called after routes were registered, so microservice.From(c) will be nil in those handlers — move SetupClients before LoadModule, or inject *microservice.Clients into the controller instead",
			"routes", len(existing),
		)
	}

	app.Use(ClientsMiddleware(registry))
	app.RegisterSingleton(registry)
	app.OnShutdown(func(context.Context) error { return registry.Close() })

	return registry, nil
}

// ClientsMiddleware publishes the registry onto every request's context. Install
// it yourself when you build the registry by hand instead of via SetupClients.
func ClientsMiddleware(registry *Clients) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(clientsContextKey, registry)
		c.Next()
	}
}

// From returns the registry attached to the request, or nil when the middleware
// is not installed on this route.
//
// Go does not allow adding a field to gin.Context — it is gin's type — so
// `c.Microservice` is not expressible. This is the nearest equivalent.
func From(c *gin.Context) *Clients {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(clientsContextKey); ok {
		if registry, ok := value.(*Clients); ok {
			return registry
		}
	}
	return nil
}

// Call performs a request/reply from inside an HTTP handler and returns the reply
// decoded into T.
//
//	users, err := microservice.Call[res.ListUsers](c, "users-svc", "users", dto)
//
// A method cannot introduce its own type parameter in Go, so the typed form has
// to be a function rather than clients.Send[T](...). The upside is that the
// return value is a real struct instead of a map that every call site has to
// re-assert.
//
// The outbound call inherits the request's context, so a client that disconnects
// cancels the downstream call instead of leaving it to finish into a dead socket.
func Call[T any](c *gin.Context, service, pattern string, payload any) (T, error) {
	var zero T

	registry := From(c)
	if registry == nil {
		return zero, errors.New(
			"microservice: no client registry on this request — call microservice.SetupClients(app, ...) before LoadModule, or inject *microservice.Clients into the controller",
		)
	}

	client, err := registry.Client(service)
	if err != nil {
		return zero, err
	}

	reply, err := client.Request(c.Request.Context(), pattern, payload)
	if err != nil {
		return zero, err
	}
	if reply.Error != nil {
		return zero, reply.Error
	}

	var out T
	if len(reply.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(reply.Data, &out); err != nil {
		return zero, fmt.Errorf("microservice: cannot decode the %s reply for %q into %T: %w",
			service, pattern, out, err)
	}
	return out, nil
}

// CallEmit publishes from inside an HTTP handler without waiting for a reply.
//
// Note the context: it is the *request* context, so the publish is abandoned if
// the client disconnects first. For an event that must be sent regardless of what
// the caller does, use context.WithoutCancel(c.Request.Context()) and the
// registry directly.
func CallEmit(c *gin.Context, service, pattern string, payload any) error {
	registry := From(c)
	if registry == nil {
		return errors.New(
			"microservice: no client registry on this request — call microservice.SetupClients(app, ...) before LoadModule",
		)
	}
	return registry.Emit(c.Request.Context(), service, pattern, payload)
}
