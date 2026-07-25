// Package rabbitmq implements the microservice transport over AMQP 0-9-1, using
// a topic exchange.
//
// A topic exchange is the shape AMQP was designed for and the only one that maps
// onto this framework's pattern model: publishers address a subject, the broker
// decides which queues want it, and adding a consumer requires no change on the
// publisher. Queues are the unit of work distribution — every replica of a
// service consumes the same queue, so the broker load-balances between them; two
// different services use two different queues and both see the message.
//
// Routing keys are the subtle part; see routing.go for why a character-level
// pattern like "user_*" cannot be expressed as an AMQP binding key and what this
// transport does instead.
//
// Delivery semantics, in one place:
//
//   - Publishing uses publisher confirms by default. A bare AMQP publish is
//     fire-and-forget even towards the broker: it is a one-way frame, so a
//     rejected or dropped message looks exactly like a successful one. Confirms
//     make Publish report the broker's verdict. Set DisableConfirms to trade that
//     for throughput.
//   - Consuming uses manual acknowledgement by default. A message is acked after
//     the handler succeeds, so a consumer crash mid-handler redelivers rather
//     than loses — at-least-once.
//   - A handler failure nacks without requeue, so a message that fails
//     deterministically cannot spin the consumer in a hot loop. Configure
//     DeadLetterExchange to keep those messages; set Requeue to opt into
//     redelivery when your failures are transient.
package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nika-framework/nika/common/microservice"
)

// Defaults applied to a zero Options.
const (
	// DefaultExchange is the exchange every Nika service uses unless told
	// otherwise.
	DefaultExchange = "nika"

	// DefaultExchangeType is a topic exchange; see the package comment.
	DefaultExchangeType = "topic"

	// DefaultQueue is a durable, named, shared queue. See Options.Queue for why
	// the default is emphatically not an anonymous one.
	DefaultQueue = "nika.workers"

	// DefaultPrefetch bounds unacknowledged deliveries per consumer. Without a
	// QoS prefetch the broker pushes the whole queue at whichever consumer
	// connected first and every other replica idles with an empty queue.
	DefaultPrefetch = 32

	// DefaultHeartbeat detects a dead peer. AMQP over a NAT or a load balancer
	// dies silently without one.
	DefaultHeartbeat = 10 * time.Second
)

// Options configures a RabbitMQ transport.
type Options struct {
	// URL is the AMQP dial string, e.g. "amqp://guest:guest@localhost:5672/".
	// Ignored when Conn is set.
	URL string

	// Conn reuses an existing connection instead of dialling. Ownership stays
	// with the caller: Close closes this transport's own channels but never this
	// connection, because a connection shared with the rest of the process must
	// not be torn down by one of its users.
	Conn *amqp.Connection

	// Exchange is the topic exchange to publish to and bind against. Defaults to
	// DefaultExchange.
	Exchange string

	// ExchangeType defaults to DefaultExchangeType. Anything other than "topic"
	// disables pattern routing at the broker and leaves all filtering to the
	// Router.
	ExchangeType string

	// Queue is this service's queue name. It defaults to DefaultQueue — a
	// durable, *named* queue — rather than an anonymous server-generated one.
	//
	// This is the single most expensive mistake to make with AMQP. An anonymous
	// auto-delete queue exists only while its consumer is connected, so every
	// message published during a deploy, a crash loop or a broker reconnect is
	// dropped by the broker with no error anywhere: the publisher's confirm
	// succeeds, the exchange matches nothing, and the message is gone. A durable
	// named queue keeps accumulating while the service is down and drains when it
	// returns.
	//
	// Every service sharing an Exchange must set a distinct Queue. Two services
	// on one queue compete for messages instead of both receiving them.
	Queue string

	// Durable declares a topology that survives a broker restart. Nil means
	// true; use Bool(false) to opt out. It is a pointer precisely because the
	// safe value is not the zero value.
	Durable *bool

	// AutoDelete deletes the queue when its last consumer disconnects. Leave it
	// false unless the queue really is disposable; see Queue.
	AutoDelete bool

	// QueueArgs are extra x-arguments for the queue declaration (x-max-length,
	// x-queue-type, x-message-ttl, …). DeadLetterExchange is merged into these.
	QueueArgs amqp.Table

	// DeadLetterExchange declares the queue with x-dead-letter-exchange, so a
	// message this transport nacks is republished there instead of discarded.
	// This is the supported way to keep poison messages: an operator can inspect
	// and replay them without the consumer ever retrying in a loop.
	//
	// Changing it on an existing queue requires deleting and redeclaring the
	// queue — AMQP queue arguments are immutable, and a mismatched redeclaration
	// fails the channel with PRECONDITION_FAILED.
	DeadLetterExchange string

	// Prefetch is the QoS prefetch count. Defaults to DefaultPrefetch.
	Prefetch int

	// Concurrency bounds deliveries handled at once by this transport. Defaults
	// to Prefetch, since a prefetch larger than the concurrency just parks
	// messages in a client-side buffer where they are invisible to the broker's
	// queue depth metrics.
	Concurrency int

	// AutoAck acknowledges on delivery instead of after the handler. It defaults
	// to false: with AutoAck the broker forgets the message as soon as it hits
	// the socket, so a crash loses everything in flight — at-most-once.
	AutoAck bool

	// Requeue makes a failed handler nack with requeue=true.
	//
	// Default false. A requeued message goes back to the head of the queue and is
	// redelivered immediately, so a message that fails deterministically — a
	// malformed payload, a bug, a missing row — is redelivered forever at the
	// speed of the CPU. That is the classic AMQP poison-message outage. Turn this
	// on only when your handler failures are genuinely transient, and pair it
	// with DeadLetterExchange plus a queue x-delivery-limit so the broker itself
	// caps the retries.
	//
	// A retry counter derived from the x-death header is deliberately not
	// implemented: x-death only exists after the broker has already dead-lettered
	// the message, so counting it in the consumer reimplements broker policy in
	// application code and silently stops working when an operator changes the
	// dead-letter topology. Retry limits belong in the queue definition.
	Requeue bool

	// Mandatory asks the broker to return a message that matched no queue.
	// Returns are logged, not surfaced from Publish: a publisher confirm proves
	// the broker accepted a message, never that anybody was bound to receive it,
	// and an unroutable message is both Returned and Acked.
	Mandatory bool

	// DisableConfirms turns off publisher confirms, making Publish return as soon
	// as the frame is written. Faster, and it can no longer tell you that the
	// broker refused the message.
	DisableConfirms bool

	// ReplyTimeout bounds a Request when the caller passes no timeout. Defaults
	// to microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// TLSConfig is used when URL has the amqps scheme.
	TLSConfig *tls.Config

	// Heartbeat is the AMQP heartbeat interval. Defaults to DefaultHeartbeat.
	Heartbeat time.Duration

	// Logger receives malformed-message and reconnect events. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Bool returns a pointer to v, for Options fields whose safe default is not the
// zero value.
func Bool(v bool) *bool { return &v }

// Transport is a RabbitMQ transport. It is safe for concurrent use.
type Transport struct {
	opts Options
	log  *slog.Logger

	// ownsConn is false when the caller supplied Options.Conn, in which case
	// Close must leave the connection alone.
	ownsConn bool

	mu   sync.Mutex
	conn *amqp.Connection
	sess *session

	// pending correlates an in-flight Request with the channel its reply will
	// arrive on. Every entry is removed in a defer on every exit path, including
	// timeout and connection loss, so a peer that never answers cannot grow the
	// map.
	pendingMu sync.Mutex
	pending   map[string]chan *microservice.Envelope

	closeOnce sync.Once
	closed    chan struct{}
}

// New returns a RabbitMQ transport.
//
// It does not dial. Connections are established lazily and re-established after
// a drop, so constructing a transport never fails because the broker happens to
// be restarting, and a wiring mistake surfaces as a config error rather than a
// timeout.
func New(opts Options) (*Transport, error) {
	if opts.Conn == nil && opts.URL == "" {
		return nil, errors.New("rabbitmq: Options needs a URL or an existing Conn")
	}
	if opts.Exchange == "" {
		opts.Exchange = DefaultExchange
	}
	if opts.ExchangeType == "" {
		opts.ExchangeType = DefaultExchangeType
	}
	if opts.Queue == "" {
		opts.Queue = DefaultQueue
	}
	if opts.Durable == nil {
		opts.Durable = Bool(true)
	}
	if opts.Prefetch <= 0 {
		opts.Prefetch = DefaultPrefetch
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = opts.Prefetch
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = DefaultHeartbeat
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &Transport{
		opts:     opts,
		log:      opts.Logger,
		ownsConn: opts.Conn == nil,
		conn:     opts.Conn,
		pending:  make(map[string]chan *microservice.Envelope),
		closed:   make(chan struct{}),
	}, nil
}

// MustNew is New, panicking on a configuration error. Use it in package-level
// wiring where there is nothing useful to do with the error anyway.
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportRabbitMQ }

// Close releases this transport's channels and, when it dialled the connection
// itself, the connection. It is idempotent and unblocks every in-flight Request
// and Listen.
func (t *Transport) Close() error {
	var firstErr error

	t.closeOnce.Do(func() {
		// Closing first means every waiter is released even if tearing down the
		// AMQP objects blocks or errors.
		close(t.closed)

		t.mu.Lock()
		sess, conn, owns := t.sess, t.conn, t.ownsConn
		t.sess = nil
		t.mu.Unlock()

		if sess != nil {
			if err := sess.close(); err != nil && !isClosedErr(err) {
				firstErr = err
			}
		}
		if owns && conn != nil {
			if err := conn.Close(); err != nil && !isClosedErr(err) && firstErr == nil {
				firstErr = err
			}
		}

		t.pendingMu.Lock()
		t.pending = make(map[string]chan *microservice.Envelope)
		t.pendingMu.Unlock()
	})

	return firstErr
}

// isClosed reports whether Close has run.
func (t *Transport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// pendingLen reports the number of in-flight correlation entries. It exists so
// tests can assert that a timed-out or cancelled Request leaves nothing behind.
func (t *Transport) pendingLen() int {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return len(t.pending)
}

// durable reports the resolved Durable option.
func (t *Transport) durable() bool { return t.opts.Durable == nil || *t.opts.Durable }

// queueArgs builds the queue x-arguments, merging DeadLetterExchange into any
// caller-supplied table without mutating it.
func (t *Transport) queueArgs() amqp.Table {
	if len(t.opts.QueueArgs) == 0 && t.opts.DeadLetterExchange == "" {
		return nil
	}
	args := make(amqp.Table, len(t.opts.QueueArgs)+1)
	for k, v := range t.opts.QueueArgs {
		args[k] = v
	}
	if t.opts.DeadLetterExchange != "" {
		args["x-dead-letter-exchange"] = t.opts.DeadLetterExchange
	}
	return args
}

// connection returns a live connection, dialling or redialling when needed.
// Callers must not hold t.mu.
func (t *Transport) connection() (*amqp.Connection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connectionLocked()
}

func (t *Transport) connectionLocked() (*amqp.Connection, error) {
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}
	if t.conn != nil && !t.conn.IsClosed() {
		return t.conn, nil
	}
	if !t.ownsConn {
		// The caller handed us a connection and then closed it. Redialling would
		// silently take over a lifecycle we were told not to own.
		return nil, fmt.Errorf("rabbitmq: the caller-owned connection is closed")
	}

	conn, err := amqp.DialConfig(t.opts.URL, amqp.Config{
		Heartbeat:       t.opts.Heartbeat,
		TLSClientConfig: t.opts.TLSConfig,
		Properties:      amqp.Table{"connection_name": "nika/" + t.opts.Queue},
	})
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: dial %s: %w", safeURL(t.opts.URL), err)
	}
	t.conn = conn
	return conn, nil
}

// declareExchange is idempotent, and cheap enough to repeat on every new channel
// rather than tracking whether some other process already did it.
func (t *Transport) declareExchange(ch *amqp.Channel) error {
	err := ch.ExchangeDeclare(t.opts.Exchange, t.opts.ExchangeType, t.durable(),
		false /* autoDelete */, false /* internal */, false /* noWait */, nil)
	if err != nil {
		return fmt.Errorf("rabbitmq: declare exchange %q: %w", t.opts.Exchange, err)
	}
	return nil
}

// mapTimeout normalises a context deadline onto microservice.ErrTimeout so
// callers can test for a timeout without knowing which layer produced it.
func mapTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return microservice.ErrTimeout
	}
	return err
}

// isClosedErr reports whether err is just "already closed", which Close must not
// report as a failure.
func isClosedErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, amqp.ErrClosed) {
		return true
	}
	var aerr *amqp.Error
	return errors.As(err, &aerr) && aerr.Code == amqp.ChannelError
}

// safeURL strips credentials from an AMQP URL so a dial failure can be logged
// without leaking the password.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "amqp://<redacted>"
	}
	if u.User != nil {
		u.User = url.User("<redacted>")
	}
	return u.String()
}
