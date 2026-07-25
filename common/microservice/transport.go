package microservice

import (
	"context"
	"errors"
	"time"
)

// Transport names, usable as the value of a `transport:"..."` struct tag.
const (
	TransportRedis    = "redis"
	TransportNATS     = "nats"
	TransportRabbitMQ = "rabbitmq"
	TransportKafka    = "kafka"
	TransportGRPC     = "grpc"
	TransportTCP      = "tcp"
	TransportMemory   = "memory"
)

// Common errors callers can test for.
var (
	// ErrNoHandler means no registered pattern matched the subject.
	ErrNoHandler = errors.New("microservice: no handler registered for pattern")

	// ErrTimeout means a request/reply exchange exceeded its deadline.
	ErrTimeout = errors.New("microservice: request timed out")

	// ErrClosed means the transport was closed.
	ErrClosed = errors.New("microservice: transport is closed")

	// ErrNotSupported means the transport cannot perform the operation — for
	// example request/reply over a fire-and-forget Kafka topic without a
	// configured reply topic.
	ErrNotSupported = errors.New("microservice: operation not supported by this transport")
)

// Dispatcher receives an inbound envelope and returns the reply envelope.
//
// A Dispatcher never returns a Go error for an application-level failure: the
// failure is encoded in the returned envelope's Status and Error so it can cross
// the wire. An error return means the message itself was unusable and the
// transport should nack or drop it.
type Dispatcher func(ctx context.Context, env *Envelope) (*Envelope, error)

// Listener is the server half of a transport: it subscribes to the declared
// patterns and feeds every inbound message to the dispatcher.
type Listener interface {
	// Name returns the transport name, matching the `transport` tag value.
	Name() string

	// Listen begins consuming and blocks until ctx is cancelled or a fatal
	// error occurs. Returning nil means a clean shutdown.
	//
	// patterns are the handler patterns registered for this transport. A
	// transport with native pattern subscriptions (Redis PSUBSCRIBE, NATS
	// wildcards, a RabbitMQ topic exchange) can use them to filter at the
	// broker; a transport without them subscribes to its configured address and
	// relies on the Router.
	Listen(ctx context.Context, patterns []string, dispatch Dispatcher) error

	// Close releases the connection. It must be safe to call more than once.
	Close() error
}

// Publisher is the client half of a transport.
type Publisher interface {
	// Name returns the transport name.
	Name() string

	// Publish sends an envelope without waiting for a reply.
	Publish(ctx context.Context, env *Envelope) error

	// Request sends an envelope and waits for the correlated reply. A transport
	// that cannot do request/reply returns ErrNotSupported.
	Request(ctx context.Context, env *Envelope, timeout time.Duration) (*Envelope, error)

	// Close releases the connection. It must be safe to call more than once.
	Close() error
}

// Transport is a bidirectional transport, able to both serve and publish.
type Transport interface {
	Listener
	Publisher
}

// DefaultRequestTimeout bounds a request/reply exchange when the caller does not
// supply one. An unbounded request would pin a goroutine and a correlation entry
// forever if the peer never answers.
const DefaultRequestTimeout = 10 * time.Second
