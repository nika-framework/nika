// Package redismq implements the microservice transport over Redis pub/sub.
//
// # Delivery guarantees
//
// This is the caveat that decides whether this transport is the right one, so it
// comes first: Redis pub/sub is at-most-once and stores nothing. A message is
// delivered to the subscribers connected at the instant it is published and then
// forgotten. A consumer that is restarting, deploying, GC-paused past the client's
// buffer, or briefly disconnected does not receive it later — it never receives it,
// and no error is reported to the publisher, because PUBLISH to a channel with no
// subscribers is a success that returns 0.
//
// That makes it an excellent fit for cache invalidation, presence, live dashboards
// and other traffic where the next message supersedes the last, and a poor fit for
// anything a business depends on. When delivery has to survive a consumer restart,
// use a log with consumer groups — Redis Streams with XADD/XREADGROUP, or a
// broker-backed transport — so unacknowledged messages are still there afterwards.
//
// # Model
//
// One Redis channel per pattern, namespaced by a prefix. Literal patterns are
// SUBSCRIBEd and wildcard patterns are PSUBSCRIBEd, so the broker does the
// filtering and the process only receives what it asked for.
package redismq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nika-framework/nika/common/microservice"
)

// Transport implements the full bidirectional contract; asserting it here turns a
// signature drift in the microservice package into a compile error in this package
// rather than a runtime surprise at the Setup call site.
var _ microservice.Transport = (*Transport)(nil)

// Options configures a Redis transport.
type Options struct {
	// URL is a Redis connection string ("redis://:pass@host:6379/0",
	// "rediss://..." for TLS). Required unless Client is supplied.
	URL string

	// Client reuses an existing client instead of building one from URL.
	//
	// Ownership stays with the caller: Close does not close a client it did not
	// create. Closing a shared client would tear down the pool the rest of the
	// application is using, which is a far worse failure than leaking one
	// connection at shutdown.
	Client *redis.Client

	// Prefix namespaces every channel as prefix + ":" + pattern. Defaults to
	// "nika". Redis pub/sub has no vhosts and PUBLISH ignores the selected
	// database, so this prefix is the only thing keeping two services on one
	// instance from receiving each other's traffic.
	Prefix string

	// Concurrency caps messages dispatched at once. Defaults to 64. Reaching the
	// cap pauses reads from the subscription channel, which go-redis buffers; if
	// that buffer stays full for a minute go-redis drops messages, so a
	// permanently saturated consumer loses traffic rather than queueing it.
	Concurrency int

	// ReplyTimeout is the default request/reply deadline when a caller passes
	// none. Defaults to microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// PingTimeout bounds the health check. Defaults to 2s.
	PingTimeout time.Duration

	// HealthCheck makes New verify connectivity with a PING and fail if the
	// server is unreachable. It is off by default because a transport is normally
	// constructed while wiring the application, when Redis may legitimately not be
	// up yet; turn it on when you would rather the process refuse to start.
	HealthCheck bool

	// Logger receives decode failures and subscription errors. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// DefaultPrefix namespaces channels when Options.Prefix is empty.
const DefaultPrefix = "nika"

const (
	defaultConcurrency = 64
	defaultPingTimeout = 2 * time.Second
)

// Transport is a bidirectional Redis pub/sub transport. It satisfies
// microservice.Transport.
type Transport struct {
	client     *redis.Client
	ownsClient bool

	prefix       string
	concurrency  int
	replyTimeout time.Duration
	pingTimeout  time.Duration
	log          *slog.Logger

	// replyChannel is this client's private inbox. It carries a random 128-bit id
	// so it is unguessable: replies travel over a channel any Redis client can
	// subscribe to, and a predictable inbox name would let one process harvest
	// another's replies — which may carry authenticated payloads.
	clientID     string
	replyChannel string

	closeOnce sync.Once
	closed    chan struct{}

	replyMu  sync.Mutex
	replySub *redis.PubSub
	replyWG  sync.WaitGroup

	// pending correlates a reply with the Request goroutine waiting for it.
	pendingMu sync.Mutex
	pending   map[string]chan *microservice.Envelope
}

// New validates the options and returns a transport.
//
// A malformed URL fails here, at startup, rather than on the first published
// message. Connectivity is only verified when Options.HealthCheck is set: go-redis
// pools lazily, and requiring a live server at construction time would make
// application wiring order depend on infrastructure readiness. Call Ping when you
// want to check on your own schedule.
func New(opts Options) (*Transport, error) {
	if opts.URL == "" && opts.Client == nil {
		return nil, errors.New("redismq: either Options.URL or Options.Client is required")
	}
	if opts.URL != "" && opts.Client != nil {
		return nil, errors.New("redismq: set either Options.URL or Options.Client, not both")
	}
	if opts.Concurrency < 0 {
		return nil, errors.New("redismq: Options.Concurrency cannot be negative")
	}

	if opts.Prefix == "" {
		opts.Prefix = DefaultPrefix
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.PingTimeout <= 0 {
		opts.PingTimeout = defaultPingTimeout
	}

	client := opts.Client
	ownsClient := false
	if client == nil {
		parsed, err := redis.ParseURL(opts.URL)
		if err != nil {
			return nil, fmt.Errorf("redismq: invalid Options.URL: %w", err)
		}
		client = redis.NewClient(parsed)
		ownsClient = true
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	clientID := microservice.NewID()
	t := &Transport{
		client:       client,
		ownsClient:   ownsClient,
		prefix:       opts.Prefix,
		concurrency:  opts.Concurrency,
		replyTimeout: opts.ReplyTimeout,
		pingTimeout:  opts.PingTimeout,
		log:          log.With(slog.String("transport", microservice.TransportRedis)),
		clientID:     clientID,
		replyChannel: opts.Prefix + ":" + replyNamespace + clientID,
		closed:       make(chan struct{}),
		pending:      make(map[string]chan *microservice.Envelope),
	}

	if opts.HealthCheck {
		ctx, cancel := context.WithTimeout(context.Background(), t.pingTimeout)
		defer cancel()
		if err := t.Ping(ctx); err != nil {
			if ownsClient {
				_ = client.Close()
			}
			return nil, err
		}
	}

	return t, nil
}

// MustNew is New for the one-line setup case, where a bad option is a programming
// error rather than a runtime condition:
//
//	microservice.Setup(app, microservice.Config{
//	    Transport: redismq.MustNew(redismq.Options{URL: "redis://localhost:6379"}),
//	})
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportRedis }

// Ping checks that the server answers. It is the health check to wire into a
// readiness probe.
func (t *Transport) Ping(ctx context.Context) error {
	if t.isClosed() {
		return microservice.ErrClosed
	}
	if t.pingTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.pingTimeout)
		defer cancel()
	}
	if err := t.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redismq: ping: %w", mapTimeout(err))
	}
	return nil
}

// ReplyChannel returns this client's private reply inbox, for diagnostics.
func (t *Transport) ReplyChannel() string { return t.replyChannel }

// Close stops the transport, unblocks every pending Request and tears down the
// reply subscription. It is safe to call concurrently and more than once.
func (t *Transport) Close() error {
	var err error

	t.closeOnce.Do(func() {
		close(t.closed)

		t.replyMu.Lock()
		sub := t.replySub
		t.replySub = nil
		t.replyMu.Unlock()

		if sub != nil {
			if closeErr := sub.Close(); closeErr != nil && !errors.Is(closeErr, redis.ErrClosed) {
				err = fmt.Errorf("redismq: closing reply subscription: %w", closeErr)
			}
		}

		// Only a client we created is ours to close; see Options.Client.
		if t.ownsClient {
			if closeErr := t.client.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("redismq: closing client: %w", closeErr)
			}
		}
	})

	t.replyWG.Wait()
	return err
}

func (t *Transport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// mapTimeout normalises a deadline error onto the transport-agnostic sentinel so
// callers can test for a timeout without knowing which layer produced it.
func mapTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return microservice.ErrTimeout
	}
	return err
}

// errorReply builds a failure reply for a message that produced none, so a waiting
// client gets an answer instead of a timeout. The microservice package keeps its
// own version of this unexported, hence the local copy.
func errorReply(env *microservice.Envelope, status int, code, detail string) *microservice.Envelope {
	return &microservice.Envelope{
		Pattern: env.Pattern,
		ID:      env.ID,
		Status:  status,
		Error: &microservice.EnvelopeError{
			Code:    status,
			Message: code,
			Details: detail,
		},
	}
}

// pendingLen reports the number of outstanding correlation entries. It exists so
// tests can assert that a timed-out Request leaves nothing behind: a correlation
// map cleaned only on the success path leaks one entry per unanswered call, and a
// peer that simply stops replying can then grow it without bound.
func (t *Transport) pendingLen() int {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return len(t.pending)
}
