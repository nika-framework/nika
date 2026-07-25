// Package natsmq implements the microservice transport over NATS.
//
// NATS is the best-matched broker for this layer: it has native request/reply with
// server-side correlation, so this transport hand-rolls no correlation map at all,
// and it reports "no responders" immediately instead of making a caller wait out a
// timeout to discover that nobody is listening.
//
// What it does not have is character-level wildcards. NATS subjects are
// dot-separated tokens and its wildcards work on whole tokens, while
// microservice.Pattern matches characters. See subjectPlan for how that gap is
// resolved and what it costs.
//
// Delivery is at-most-once on core NATS: a subscriber that is down misses messages,
// exactly as with Redis pub/sub. NATS JetStream adds persistence and
// acknowledgement, and is the right answer when a message must survive a consumer
// restart.
package natsmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/nika-framework/nika/common/microservice"
)

// DefaultPrefix namespaces subjects when Options.Prefix is empty.
const DefaultPrefix = "nika"

// NoQueueGroup forces broadcast delivery even when Options.Name is set, overriding
// the default described on Options.QueueGroup.
const NoQueueGroup = "-"

const (
	defaultConcurrency   = 64
	defaultReconnectWait = 2 * time.Second
	defaultDrainTimeout  = 30 * time.Second
)

// Transport implements the full bidirectional contract; asserting it here turns a
// signature drift in the microservice package into a compile error in this package
// rather than a runtime surprise at the Setup call site.
var _ microservice.Transport = (*Transport)(nil)

// Options configures a NATS transport.
type Options struct {
	// URL is a NATS server URL, or a comma-separated list for a cluster. Defaults
	// to nats.DefaultURL.
	URL string

	// Conn reuses an existing connection instead of dialing.
	//
	// Ownership stays with the caller: Close neither drains nor closes a
	// connection it did not create, because doing so would cut off whatever else
	// in the process shares it.
	Conn *nats.Conn

	// Prefix namespaces every subject as prefix + "." + pattern. Defaults to
	// "nika". The NATS subject space is flat and global to an account, so this is
	// what stops two unrelated services from receiving each other's messages.
	Prefix string

	// QueueGroup decides the single most consequential behaviour of this
	// transport, so it is worth being explicit about:
	//
	//   set    — replicas join a queue group and NATS delivers each message to
	//            exactly one of them. This is a load-balanced service. Scaling out
	//            adds throughput.
	//   empty  — every replica receives every message. This is a broadcast. Scaling
	//            out multiplies the work, and any non-idempotent handler now runs
	//            once per replica.
	//
	// Defaulting: when QueueGroup is empty and Name is set, Name is adopted as the
	// queue group, because a named service with several replicas almost always
	// wants load balancing and the broadcast failure mode is silent and expensive.
	// Set QueueGroup to NoQueueGroup to force broadcast anyway.
	QueueGroup string

	// Concurrency caps messages dispatched at once. Defaults to 64. Reaching the
	// cap stops draining the subscription, and NATS enforces its own pending
	// limits on an unread subscription — a consumer that stays saturated is
	// reported as a slow consumer and its messages are dropped, not queued.
	Concurrency int

	// ReplyTimeout is the default request/reply deadline when a caller passes
	// none. Defaults to microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// Name identifies this connection in `nats server report connections`, which
	// is the difference between a diagnosable cluster and a list of anonymous
	// sockets. It also seeds QueueGroup; see above.
	Name string

	// Token authenticates with a bearer token.
	Token string

	// User and Password authenticate with credentials.
	User     string
	Password string

	// NKeySeed is a raw nkey seed ("SU..."). It is the ed25519 private key: it
	// never leaves the process, and the server is convinced by a signature over a
	// per-connection nonce rather than by a shared secret it could leak.
	NKeySeed string

	// CredsFile is the path to a NATS JWT credentials file, the usual choice with
	// a managed or multi-tenant deployment.
	CredsFile string

	// TLSConfig is passed to the server connection. Use it for mTLS or a private
	// CA; a "tls://" or "nats://" URL alone does not pin anything.
	TLSConfig *tls.Config

	// ReconnectWait is the pause between reconnect attempts. Defaults to 2s.
	ReconnectWait time.Duration

	// DrainTimeout bounds a graceful shutdown. Defaults to 30s.
	DrainTimeout time.Duration

	// LazyConnect defers dialing until the first Listen, Publish or Request
	// instead of connecting in New.
	//
	// The default is eager, so an unreachable server or bad credentials fail at
	// startup where they are easy to see. Set this when the process must be able
	// to start before the broker is reachable — and in tests, where it makes the
	// transport constructible with no server running.
	LazyConnect bool

	// Logger receives connection lifecycle and decode events. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Transport is a bidirectional NATS transport. It satisfies
// microservice.Transport.
type Transport struct {
	url          string
	prefix       string
	queueGroup   string
	concurrency  int
	replyTimeout time.Duration
	drainTimeout time.Duration
	natsOpts     []nats.Option
	log          *slog.Logger

	closeOnce sync.Once
	closed    chan struct{}

	connMu   sync.Mutex
	nc       *nats.Conn
	ownsConn bool

	// connClosed is closed by the nats ClosedHandler, letting Close wait for a
	// Drain to actually finish instead of guessing.
	connClosed chan struct{}

	// handlers counts in-flight dispatch goroutines. NATS's own Drain waits for
	// its callbacks to return, and ours return immediately after handing the
	// message to a goroutine, so draining the connection alone would not wait for
	// any real work.
	handlers sync.WaitGroup
}

// New validates the options and connects.
//
// Connecting here is deliberate: NATS reports authentication failures, TLS
// problems and unreachable servers on connect, and those are startup problems, not
// per-message problems. Set Options.LazyConnect to defer it.
func New(opts Options) (*Transport, error) {
	if opts.URL != "" && opts.Conn != nil {
		return nil, errors.New("natsmq: set either Options.URL or Options.Conn, not both")
	}
	if opts.Concurrency < 0 {
		return nil, errors.New("natsmq: Options.Concurrency cannot be negative")
	}

	if opts.Prefix == "" {
		opts.Prefix = DefaultPrefix
	}
	if err := validatePrefix(opts.Prefix); err != nil {
		return nil, err
	}
	if opts.URL == "" {
		opts.URL = nats.DefaultURL
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.ReconnectWait <= 0 {
		opts.ReconnectWait = defaultReconnectWait
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = defaultDrainTimeout
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With(slog.String("transport", microservice.TransportNATS))

	t := &Transport{
		url:          opts.URL,
		prefix:       opts.Prefix,
		queueGroup:   resolveQueueGroup(opts.QueueGroup, opts.Name),
		concurrency:  opts.Concurrency,
		replyTimeout: opts.ReplyTimeout,
		drainTimeout: opts.DrainTimeout,
		log:          log,
		closed:       make(chan struct{}),
		connClosed:   make(chan struct{}),
	}

	if opts.Conn != nil {
		t.nc = opts.Conn
		t.ownsConn = false
		return t, nil
	}

	natsOpts, err := connectOptions(&opts, t, log)
	if err != nil {
		return nil, err
	}
	t.natsOpts = natsOpts
	t.ownsConn = true

	if !opts.LazyConnect {
		if _, err := t.conn(); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// MustNew is New for the one-line setup case.
//
//	microservice.Setup(app, microservice.Config{
//	    Transport: natsmq.MustNew(natsmq.Options{URL: "nats://localhost:4222", Name: "users"}),
//	})
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// resolveQueueGroup implements the defaulting documented on Options.QueueGroup.
func resolveQueueGroup(queueGroup, name string) string {
	switch queueGroup {
	case NoQueueGroup:
		return ""
	case "":
		return name
	default:
		return queueGroup
	}
}

// connectOptions builds the nats.Option list. Every resilience setting is explicit
// rather than left to the library default, because these are the settings that
// decide how the service behaves during an incident.
func connectOptions(opts *Options, t *Transport, log *slog.Logger) ([]nats.Option, error) {
	natsOpts := []nats.Option{
		// Never stop trying to reconnect. The library default gives up after a
		// handful of attempts and then closes the connection permanently, which
		// turns a broker restart into a service that stays broken until someone
		// redeploys it.
		nats.MaxReconnects(-1),
		nats.ReconnectWait(opts.ReconnectWait),
		nats.DrainTimeout(opts.DrainTimeout),

		// Buffer messages published while disconnected instead of failing them,
		// and resubscribe automatically on reconnect (both are library behaviour;
		// the handlers below make the transitions visible, because a silent
		// reconnect loop is indistinguishable from a healthy connection in a log).
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("natsmq disconnected", slog.Any("error", err))
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info("natsmq reconnected", slog.String("url", nc.ConnectedUrl()))
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Info("natsmq connection closed")
			t.signalConnClosed()
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			// This is where a slow consumer shows up. It means messages were
			// dropped, so it is an error, not a warning.
			log.Error("natsmq subscription error",
				slog.String("subject", subject),
				slog.Any("error", err),
			)
		}),
	}

	if opts.Name != "" {
		natsOpts = append(natsOpts, nats.Name(opts.Name))
	}
	if opts.TLSConfig != nil {
		natsOpts = append(natsOpts, nats.Secure(opts.TLSConfig))
	}
	if opts.Token != "" {
		natsOpts = append(natsOpts, nats.Token(opts.Token))
	}
	if opts.User != "" || opts.Password != "" {
		natsOpts = append(natsOpts, nats.UserInfo(opts.User, opts.Password))
	}
	if opts.CredsFile != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(opts.CredsFile))
	}
	if opts.NKeySeed != "" {
		kp, err := nkeys.FromSeed([]byte(opts.NKeySeed))
		if err != nil {
			return nil, fmt.Errorf("natsmq: invalid Options.NKeySeed: %w", err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("natsmq: cannot derive the public key from Options.NKeySeed: %w", err)
		}
		natsOpts = append(natsOpts, nats.Nkey(pub, func(nonce []byte) ([]byte, error) {
			return kp.Sign(nonce)
		}))
	}

	return natsOpts, nil
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportNATS }

// QueueGroup returns the effective queue group, after the defaulting described on
// Options.QueueGroup. Empty means broadcast.
func (t *Transport) QueueGroup() string { return t.queueGroup }

// conn returns the connection, dialing on first use when LazyConnect was set.
func (t *Transport) conn() (*nats.Conn, error) {
	t.connMu.Lock()
	defer t.connMu.Unlock()

	if t.isClosed() {
		return nil, microservice.ErrClosed
	}
	if t.nc != nil && !t.nc.IsClosed() {
		return t.nc, nil
	}
	if !t.ownsConn {
		// A caller-supplied connection that has been closed is the caller's
		// problem; redialing it here would silently take over ownership.
		return nil, fmt.Errorf("natsmq: %w", nats.ErrConnectionClosed)
	}

	nc, err := nats.Connect(t.url, t.natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("natsmq: connect to %q: %w", t.url, err)
	}
	t.nc = nc
	t.log.Info("natsmq connected",
		slog.String("url", nc.ConnectedUrl()),
		slog.String("queue_group", t.queueGroup),
	)
	return nc, nil
}

func (t *Transport) signalConnClosed() {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	select {
	case <-t.connClosed:
	default:
		close(t.connClosed)
	}
}

// Ping reports whether the connection is usable, for a readiness probe.
func (t *Transport) Ping(ctx context.Context) error {
	if t.isClosed() {
		return microservice.ErrClosed
	}
	nc, err := t.conn()
	if err != nil {
		return err
	}
	// FlushWithContext round-trips a PING and waits for the PONG, so it proves the
	// server is answering rather than merely that a socket exists.
	if err := nc.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("natsmq: ping: %w", mapNATSError(err))
	}
	return nil
}

// Close stops the transport, unblocks Listen and releases the connection.
//
// A connection this transport owns is drained rather than hard-closed: Drain
// unsubscribes, lets already-delivered messages finish, flushes anything still
// buffered, and only then closes. A plain Close would discard in-flight work,
// which on a request/reply subject means every caller currently waiting gets a
// timeout instead of an answer. A borrowed connection is left alone.
func (t *Transport) Close() error {
	var err error

	t.closeOnce.Do(func() {
		close(t.closed)

		t.connMu.Lock()
		nc := t.nc
		owns := t.ownsConn
		t.connMu.Unlock()

		// Wait for in-flight handlers first so their replies are still publishable.
		if !waitTimeout(&t.handlers, t.drainTimeout) {
			err = fmt.Errorf("natsmq: timed out after %s waiting for in-flight handlers", t.drainTimeout)
		}

		if nc == nil || !owns || nc.IsClosed() {
			return
		}

		if drainErr := nc.Drain(); drainErr != nil {
			nc.Close()
			if err == nil && !errors.Is(drainErr, nats.ErrConnectionClosed) {
				err = fmt.Errorf("natsmq: draining connection: %w", drainErr)
			}
			return
		}

		// Drain is asynchronous; the ClosedHandler tells us when it finished.
		select {
		case <-t.connClosed:
		case <-time.After(t.drainTimeout):
			nc.Close()
			if err == nil {
				err = fmt.Errorf("natsmq: %w", nats.ErrDrainTimeout)
			}
		}
	})

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

// waitTimeout waits for wg, reporting whether it drained in time.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// mapNATSError normalises NATS and context errors onto the transport-agnostic
// sentinels, so a caller can branch on microservice.ErrTimeout without importing
// nats.go or knowing which layer produced the failure.
func mapNATSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return microservice.ErrTimeout
	case errors.Is(err, nats.ErrNoResponders):
		// NATS answers immediately when no subscription exists, which is strictly
		// better information than a timeout: it says the message was not merely
		// unanswered but unroutable.
		return microservice.ErrNoHandler
	case errors.Is(err, nats.ErrConnectionClosed):
		return microservice.ErrClosed
	default:
		return err
	}
}

// errorReply builds a failure reply for a message that produced none, so a waiting
// client gets an answer instead of a timeout. The microservice package keeps its own
// version of this unexported, hence the local copy.
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
