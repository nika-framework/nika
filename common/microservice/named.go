package microservice

import (
	"fmt"
	"strings"
)

// Named gives a transport a service name, so `transport:"..."` tags can address a
// *service* rather than a broker type.
//
// Without it a tag names the broker ("redis", "nats"), which breaks down as soon
// as one process talks to two services over the same broker: both would be
// `transport:"redis"` and there would be no way to say which. With it, the tag
// says what the reader actually cares about:
//
//	usersTransport := microservice.Named("users-svc", redismq.MustNew(redismq.Options{...}))
//	ordersTransport := microservice.Named("orders-svc", redismq.MustNew(redismq.Options{...}))
//
//	microservice.Setup(app, microservice.Config{
//	    Transports: []microservice.Listener{usersTransport, ordersTransport},
//	})
//
//	type UserController struct {
//	    ListUser func(*gin.Context) `transport:"users-svc" pattern:"users"`
//	}
//
// The name lives here rather than in each transport's Options for two reasons: it
// is one implementation instead of six, and `Options.Name` already means
// something different on some transports — on NATS it is the connection identity
// reported by `nats server report connections`. Overloading one field name with
// two meanings across sibling packages is how configuration becomes guesswork.
func Named(name string, transport Listener) Listener {
	if transport == nil {
		panic("microservice: Named requires a transport")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		panic("microservice: Named requires a non-empty name")
	}

	// Preserve the publish half when the underlying transport has one, so a named
	// transport is still usable as a client.
	if full, ok := transport.(Transport); ok {
		return &namedTransport{name: name, Transport: full}
	}
	return &namedListener{name: name, Listener: transport}
}

// NamedPublisher gives a publisher a service name, for the client side.
func NamedPublisher(name string, publisher Publisher) Publisher {
	if publisher == nil {
		panic("microservice: NamedPublisher requires a publisher")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		panic("microservice: NamedPublisher requires a non-empty name")
	}
	return &namedPublisher{name: name, Publisher: publisher}
}

// namedListener overrides Name while delegating everything else, so a rename
// cannot change behaviour.
type namedListener struct {
	Listener
	name string
}

func (n *namedListener) Name() string { return n.name }

// Unwrap exposes the underlying transport, so a caller that needs a
// transport-specific method (redismq's Ping, tcpmq's Addr) can still reach it.
func (n *namedListener) Unwrap() Listener { return n.Listener }

type namedPublisher struct {
	Publisher
	name string
}

func (n *namedPublisher) Name() string      { return n.name }
func (n *namedPublisher) Unwrap() Publisher { return n.Publisher }

type namedTransport struct {
	Transport
	name string
}

func (n *namedTransport) Name() string { return n.name }

func (n *namedTransport) Unwrap() Transport { return n.Transport }

// Compile-time proof that the wrappers really do satisfy the interfaces they
// claim, so a future interface change fails here rather than at a call site.
var (
	_ Listener  = (*namedListener)(nil)
	_ Publisher = (*namedPublisher)(nil)
	_ Transport = (*namedTransport)(nil)
)

// TransportName reports the broker type behind a possibly-renamed transport,
// which is what a log line or a metric label wants: "users-svc failed" is less
// useful than "users-svc (redis) failed".
func TransportName(v any) string {
	type unwrapListener interface{ Unwrap() Listener }
	type unwrapPublisher interface{ Unwrap() Publisher }
	type unwrapTransport interface{ Unwrap() Transport }

	for range 8 { // bounded: a mis-built wrapper cycle must not hang a log call
		switch inner := v.(type) {
		case unwrapTransport:
			v = inner.Unwrap()
		case unwrapListener:
			v = inner.Unwrap()
		case unwrapPublisher:
			v = inner.Unwrap()
		default:
			if named, ok := v.(interface{ Name() string }); ok {
				return named.Name()
			}
			return fmt.Sprintf("%T", v)
		}
	}
	return "unknown"
}
