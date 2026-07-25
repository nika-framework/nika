// Package tcpmq implements the microservice transport over raw TCP, with no
// broker in the middle.
//
// The server binds a port and clients dial it directly. That trade is worth
// making in exactly two situations: a sidecar or tightly coupled pair of services
// where a broker would be the only piece of infrastructure in the deployment, and
// tests — a TCP transport needs nothing running, so an end-to-end test of the
// whole microservice stack costs a loopback listener instead of docker-compose.
//
// What you give up is everything a broker provides: there is no persistence, no
// fan-out to multiple consumers, no queue to absorb a consumer restart, and the
// client must know the server's address. If a message must survive the consumer
// being down, use a broker-backed transport instead.
package tcpmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

// Transport implements the full bidirectional contract; asserting it here turns a
// signature drift in the microservice package into a compile error in this package
// rather than a runtime surprise at the Setup call site.
var _ microservice.Transport = (*Transport)(nil)

// ReplyToConn is the sentinel a client puts in Envelope.ReplyTo to ask for a
// reply. On a broker the reply address is a channel or subject name, but here the
// reply address *is* the connection the request arrived on — there is nowhere else
// to send it — so a fixed sentinel is enough to distinguish "answer me" from
// "fire and forget".
const ReplyToConn = "conn"

// Options configures a TCP transport. One struct covers both halves: a process
// that only serves needs Addr, a process that only publishes needs Addr (or
// DialAddr) pointing at the server, and a process doing both needs nothing extra.
type Options struct {
	// Addr is the bind address for Listen ("" is invalid, ":4000" binds all
	// interfaces, "127.0.0.1:0" binds a free loopback port). It is also the
	// default dial target for Publish and Request.
	Addr string

	// DialAddr overrides Addr for the client half, for the common case where a
	// service binds ":4000" but reaches its peer at "peer.internal:4000".
	DialAddr string

	// TLSConfig, when set, wraps the listener with tls.NewListener. A message
	// transport carries auth headers and tenant ids in clear text otherwise, so
	// anything crossing a network boundary should set this.
	TLSConfig *tls.Config

	// ClientTLSConfig is used when dialing. Defaults to TLSConfig, which is right
	// for a symmetric mTLS setup and wrong for a server-only certificate — set it
	// explicitly when the two differ.
	ClientTLSConfig *tls.Config

	// MaxFrameBytes bounds one inbound frame. Defaults to DefaultMaxFrameBytes
	// and may not exceed it, because a larger frame can never decode into an
	// envelope anyway.
	MaxFrameBytes int

	// MaxConns caps simultaneously served connections. Defaults to 1024. An
	// unbounded accept loop is a file-descriptor exhaustion vector: a peer that
	// opens connections and never speaks costs the process one fd each until
	// accept() starts failing for everyone.
	MaxConns int

	// Concurrency caps messages being dispatched at once across all connections.
	// Defaults to 64. Reaching the cap applies backpressure by pausing reads,
	// which is what we want: TCP's own window then slows the publisher down
	// instead of the process buffering an unbounded backlog in memory.
	Concurrency int

	// ReadTimeout bounds reading a frame body once its header has arrived.
	// Defaults to 30s. A peer that sends a 1 MiB header and then one byte per
	// minute is a slow-loris; without this it holds a connection slot forever.
	ReadTimeout time.Duration

	// WriteTimeout bounds writing one reply frame. Defaults to 10s. A peer that
	// stops reading would otherwise block a handler goroutine indefinitely once
	// the socket buffer fills.
	WriteTimeout time.Duration

	// IdleTimeout bounds how long a connection may sit between frames. Defaults
	// to 5 minutes. The client redials transparently, so this is safe to keep
	// short; it is the only thing that reclaims a connection from a peer that
	// completed a handshake and then went silent.
	IdleTimeout time.Duration

	// DialTimeout bounds one dial attempt. Defaults to 5s.
	DialTimeout time.Duration

	// MaxDialAttempts bounds the redial loop for a single Publish or Request.
	// Defaults to 3. Attempts are also bounded by the caller's context, so this
	// only matters when the deadline is generous.
	MaxDialAttempts int

	// ReplyTimeout is the default request/reply deadline when the caller passes
	// none. Defaults to microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// ShutdownTimeout bounds how long Close waits for in-flight handlers.
	// Defaults to 5s.
	ShutdownTimeout time.Duration

	// OnListen is called with the bound address immediately after the listener is
	// created. It is the only way to learn the port when binding to :0.
	OnListen func(net.Addr)

	// Logger receives decode failures and connection errors. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Defaults applied to a zero Options.
const (
	defaultMaxConns        = 1024
	defaultConcurrency     = 64
	defaultReadTimeout     = 30 * time.Second
	defaultWriteTimeout    = 10 * time.Second
	defaultIdleTimeout     = 5 * time.Minute
	defaultDialTimeout     = 5 * time.Second
	defaultDialAttempts    = 3
	defaultShutdownTimeout = 5 * time.Second
)

// Transport is a bidirectional TCP transport. It satisfies
// microservice.Transport, so the same value can serve handlers and publish.
type Transport struct {
	opts Options
	log  *slog.Logger

	// closed is closed exactly once by Close and is what unblocks every waiting
	// Request, the accept loop and both read loops.
	closeOnce sync.Once
	closed    chan struct{}

	// slots is the global dispatch semaphore.
	slots chan struct{}

	// server state
	srvMu    sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	srvWG    sync.WaitGroup

	// client state
	dialMu sync.Mutex
	cc     *clientConn
	cliWG  sync.WaitGroup

	// pending correlates a reply with the Request goroutine waiting for it.
	pendingMu sync.Mutex
	pending   map[string]chan *microservice.Envelope
}

// New validates the options and returns a transport.
//
// It does not dial and does not bind: a TCP transport is normally constructed
// while wiring the application, before either peer is listening, so failing here
// on an unreachable address would make startup order significant. Configuration
// mistakes — an empty address, an oversized frame limit, a TLS config without a
// certificate — are caught here; connectivity problems surface from Listen,
// Publish or Request, where the supervisor can retry them.
func New(opts Options) (*Transport, error) {
	if opts.Addr == "" && opts.DialAddr == "" {
		return nil, errors.New("tcpmq: Options.Addr is required (a bind address to serve on, or a dial target to publish to)")
	}
	if opts.MaxFrameBytes < 0 {
		return nil, fmt.Errorf("tcpmq: Options.MaxFrameBytes cannot be negative")
	}
	if opts.MaxFrameBytes == 0 {
		opts.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if opts.MaxFrameBytes > DefaultMaxFrameBytes {
		return nil, fmt.Errorf(
			"tcpmq: Options.MaxFrameBytes %d exceeds the %d byte envelope limit, so such a frame could never decode",
			opts.MaxFrameBytes, DefaultMaxFrameBytes)
	}
	if opts.MaxConns < 0 || opts.Concurrency < 0 {
		return nil, errors.New("tcpmq: Options.MaxConns and Options.Concurrency cannot be negative")
	}

	if opts.MaxConns == 0 {
		opts.MaxConns = defaultMaxConns
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = defaultIdleTimeout
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.MaxDialAttempts == 0 {
		opts.MaxDialAttempts = defaultDialAttempts
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultShutdownTimeout
	}
	if opts.ClientTLSConfig == nil {
		opts.ClientTLSConfig = opts.TLSConfig
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	return &Transport{
		opts:    opts,
		log:     log.With(slog.String("transport", microservice.TransportTCP)),
		closed:  make(chan struct{}),
		slots:   make(chan struct{}, opts.Concurrency),
		conns:   make(map[net.Conn]struct{}),
		pending: make(map[string]chan *microservice.Envelope),
	}, nil
}

// MustNew is New for the one-line setup case, where an invalid option is a
// programming error rather than a runtime condition:
//
//	microservice.Setup(app, microservice.Config{
//	    Transport: tcpmq.MustNew(tcpmq.Options{Addr: ":4000"}),
//	})
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportTCP }

// Addr returns the address the server is bound to, or "" when it is not
// listening. Use it after Listen has started to discover the port chosen for
// a ":0" bind.
func (t *Transport) Addr() string {
	t.srvMu.Lock()
	defer t.srvMu.Unlock()
	if t.listener == nil {
		return ""
	}
	return t.listener.Addr().String()
}

// Close stops the server, drops every live connection and unblocks every pending
// Request. It is safe to call concurrently and more than once.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)

		t.srvMu.Lock()
		listener := t.listener
		conns := make([]net.Conn, 0, len(t.conns))
		for c := range t.conns {
			conns = append(conns, c)
		}
		t.srvMu.Unlock()

		if listener != nil {
			_ = listener.Close()
		}
		// Closing the sockets is what unblocks the read loops: a goroutine parked
		// in Read cannot be cancelled any other way.
		for _, c := range conns {
			_ = c.Close()
		}

		t.dialMu.Lock()
		cc := t.cc
		t.cc = nil
		t.dialMu.Unlock()
		if cc != nil {
			cc.close()
		}
	})

	// Wait for handlers and reader goroutines, but never indefinitely — a stuck
	// peer must not be able to hold shutdown hostage.
	if !waitTimeout(&t.srvWG, t.opts.ShutdownTimeout) {
		return fmt.Errorf("tcpmq: timed out after %s waiting for in-flight handlers", t.opts.ShutdownTimeout)
	}
	if !waitTimeout(&t.cliWG, t.opts.ShutdownTimeout) {
		return fmt.Errorf("tcpmq: timed out after %s waiting for the reply reader", t.opts.ShutdownTimeout)
	}
	return nil
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

// isClosed reports whether Close has been called.
func (t *Transport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// acquire takes a dispatch slot, or reports false if we are shutting down.
func (t *Transport) acquire(ctx context.Context) bool {
	select {
	case t.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-t.closed:
		return false
	}
}

func (t *Transport) release() { <-t.slots }

// mapTimeout normalises a context error onto the transport-agnostic sentinel so
// callers can test for a timeout without knowing which layer produced it.
func mapTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return microservice.ErrTimeout
	}
	return err
}

// errorReply builds a failure reply for a message that produced none, so a
// waiting client gets an answer instead of a timeout. The microservice package
// keeps its own version of this unexported, hence the local copy.
func errorReply(env *microservice.Envelope, status int, code, detail string) *microservice.Envelope {
	reply := &microservice.Envelope{
		Pattern: env.Pattern,
		ID:      env.ID,
		Status:  status,
		Error: &microservice.EnvelopeError{
			Code:    status,
			Message: code,
			Details: detail,
		},
	}
	return reply
}

// pendingLen reports the number of outstanding correlation entries. It exists so
// tests can assert that a timed-out or failed Request leaves nothing behind: a
// correlation map that is only cleaned on the success path leaks one entry per
// unanswered call, which is an unbounded memory leak driven by a peer that simply
// stops replying.
func (t *Transport) pendingLen() int {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return len(t.pending)
}
