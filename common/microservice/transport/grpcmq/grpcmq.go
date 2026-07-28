// Package grpcmq implements the microservice transport over gRPC.
//
// # No protoc
//
// The service is declared as a hand-written grpc.ServiceDesc and the payload is
// carried by a registered pass-through codec, so there is no .proto file, no
// generated .pb.go and no code generation step. See service.go and codec.go for
// the mechanism and the trade-off against real protobuf.
//
// # This is not a broker
//
// gRPC is a synchronous RPC transport, and the difference from every other
// transport in this package is not a detail:
//
//   - There is no store and forward. If the server is down when you Publish, the
//     message is lost — there is no queue to hold it and nothing to retry into.
//   - Publish still costs a full round trip. It is "fire and forget" only in the
//     sense that the reply is discarded; the TCP round trip and the server's
//     handler both happen before Publish returns.
//   - There is no fan-out. One call reaches one server.
//
// So use gRPC for synchronous service-to-service calls, where a caller is waiting
// and a failure should be visible immediately. Use Kafka, NATS or RabbitMQ for
// events, where the publisher must not care whether a consumer exists yet.
//
// What gRPC gives in exchange is the lowest-latency request/reply of any transport
// here — no correlation map, no broker hop, HTTP/2 multiplexing, deadlines
// propagated by the protocol itself, and mTLS.
package grpcmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/nika-framework/nika/common/microservice"
)

// Defaults applied to a zero Options.
const (
	// DefaultMaxRecvMsgSize is 8 MiB, matching the envelope cap in the
	// microservice package. gRPC's own default is 4 MiB, which rejects a larger
	// envelope with ResourceExhausted — a failure that shows up the first time a
	// real payload is big and looks like a bug in the handler.
	DefaultMaxRecvMsgSize = 8 << 20

	// DefaultKeepaliveMinTime is the shortest client ping interval the server
	// tolerates. Without an enforcement policy a client may ping in a tight loop,
	// which is a cheap way to burn a server's CPU from outside.
	DefaultKeepaliveMinTime = 30 * time.Second

	// DefaultConnectionTimeout bounds a connection's setup, including the TLS
	// handshake, so a half-open connection cannot occupy a slot indefinitely.
	DefaultConnectionTimeout = 20 * time.Second

	// DefaultConcurrency backstops handler concurrency across all connections.
	DefaultConcurrency = 256

	// DefaultDialTimeout bounds the wait for a usable connection on the first call.
	DefaultDialTimeout = 10 * time.Second

	// DefaultGracefulStopTimeout bounds a graceful shutdown before it is forced.
	// GracefulStop waits for every in-flight RPC and stream, and a stuck stream
	// makes it wait forever, so the fallback is not optional.
	DefaultGracefulStopTimeout = 15 * time.Second
)

// Options configures a gRPC transport. A transport may serve, call, or both.
type Options struct {
	// --- server ---

	// Addr is the listen address, e.g. ":9000" or "127.0.0.1:0". Required by
	// Listen. Port 0 asks the OS for a free port; read it back with Addr().
	Addr string

	// MaxRecvMsgSize bounds an inbound message. Defaults to
	// DefaultMaxRecvMsgSize.
	MaxRecvMsgSize int

	// MaxConcurrentStreams caps concurrent streams per HTTP/2 connection. Zero
	// leaves grpc-go's default. It is per connection, so it is not a
	// process-wide limit; see Concurrency.
	MaxConcurrentStreams uint32

	// KeepaliveMinTime is the minimum interval between client keepalive pings the
	// server will accept. Defaults to DefaultKeepaliveMinTime. A client that pings
	// faster is disconnected with ENHANCE_YOUR_CALM.
	KeepaliveMinTime time.Duration

	// KeepalivePermitWithoutStream allows pings on an idle connection. Defaults to
	// false: permitting them lets an idle client keep a connection and its
	// resources alive indefinitely for free.
	KeepalivePermitWithoutStream bool

	// ConnectionTimeout bounds connection setup. Defaults to
	// DefaultConnectionTimeout.
	ConnectionTimeout time.Duration

	// Concurrency bounds handlers running at once across every connection.
	// Defaults to DefaultConcurrency. Set it negative for no limit.
	Concurrency int

	// UnaryInterceptors and StreamInterceptors are applied in order. Because the
	// service is hand-declared, these are ordinary gRPC interceptors and work with
	// anything from the ecosystem.
	UnaryInterceptors  []grpc.UnaryServerInterceptor
	StreamInterceptors []grpc.StreamServerInterceptor

	// ServerOptions are appended last, so they can override anything above.
	ServerOptions []grpc.ServerOption

	// RegisterServices registers additional gRPC services on the same server,
	// right after the Messenger service and before the listener is served.
	//
	// This is the escape hatch from the trade-off the codec makes. The Messenger
	// service carries this framework's JSON envelope, which is what lets one
	// handler serve Redis, NATS and gRPC alike — but it is not a protobuf
	// contract, so a Node, Java or Python client generated from a .proto file
	// cannot call it. When you need that, generate the service the usual way and
	// register it here:
	//
	//	grpcmq.Options{
	//	    Addr: ":50051",
	//	    RegisterServices: func(srv *grpc.Server) {
	//	        userpb.RegisterUserServiceServer(srv, &userServer{})
	//	    },
	//	}
	//
	// The two then share one port, one set of credentials and one interceptor
	// chain. They coexist because the codec is resolved per call from the
	// content-subtype rather than forced server-wide: a protobuf client is served
	// by the protobuf codec and a Nika client by "nika-raw", over the same
	// connection.
	//
	// grpc-go panics on a duplicate service name, so registering a second
	// Messenger fails loudly at startup instead of silently shadowing it.
	RegisterServices func(srv *grpc.Server)

	// GracefulStopTimeout bounds a graceful shutdown. Defaults to
	// DefaultGracefulStopTimeout.
	GracefulStopTimeout time.Duration

	// --- client ---

	// Target is the address to call, in gRPC target syntax ("host:port",
	// "dns:///svc:9000", "unix:///tmp/s.sock"). Required by Publish and Request.
	Target string

	// DialTimeout bounds the wait for a usable connection on the first call.
	// Defaults to DefaultDialTimeout.
	DialTimeout time.Duration

	// ClientKeepalive configures client-side pings. Keep Time at or above the
	// server's KeepaliveMinTime or the server will drop the connection.
	ClientKeepalive keepalive.ClientParameters

	// WaitForReady makes a call wait for a healthy connection instead of failing
	// fast with Unavailable. It converts "the server is down" into "the call takes
	// as long as its deadline", which is right for a transient rollout and wrong
	// when a fast failure is the useful signal.
	WaitForReady bool

	// ReplyTimeout bounds a Request when the caller passes no timeout. Defaults to
	// microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// DialOptions are appended last, so they can override anything above. Use them
	// for retry policies via grpc.WithDefaultServiceConfig.
	DialOptions []grpc.DialOption

	// --- both ---

	// Creds is the transport credentials for both halves. It wins over TLSConfig.
	Creds credentials.TransportCredentials

	// TLSConfig builds credentials when Creds is nil.
	TLSConfig *tls.Config

	// Insecure must be set explicitly to run without transport security. There is
	// no implicit plaintext default: gRPC's own history of accidentally-plaintext
	// production deployments is the reason grpc-go itself now requires an explicit
	// insecure credential, and repeating that requirement here means nobody ships
	// unencrypted traffic by forgetting a field.
	Insecure bool

	// Logger receives malformed-message and lifecycle events. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Transport is a gRPC transport. It is safe for concurrent use.
type Transport struct {
	opts Options
	log  *slog.Logger

	// creds is resolved once in New so a misconfiguration fails at startup rather
	// than on the first call.
	creds credentials.TransportCredentials

	// The client connection is built on first use: a listen-only service should
	// not open a connection to itself.
	clientOnce sync.Once
	clientConn *grpc.ClientConn
	clientErr  error

	srvMu sync.Mutex
	srv   *grpc.Server
	lis   net.Listener

	readyOnce sync.Once
	ready     chan struct{}

	closeOnce sync.Once
	closed    chan struct{}
}

// New returns a gRPC transport.
//
// It neither listens nor dials; both happen on first use. Configuration errors —
// a missing address, no transport security — are reported here, because a wiring
// mistake should stop the process at startup rather than surface as a failed call
// under load.
func New(opts Options) (*Transport, error) {
	if opts.Addr == "" && opts.Target == "" {
		return nil, errors.New("grpcmq: Options needs an Addr to serve, a Target to call, or both")
	}
	if opts.MaxRecvMsgSize <= 0 {
		opts.MaxRecvMsgSize = DefaultMaxRecvMsgSize
	}
	if opts.KeepaliveMinTime <= 0 {
		opts.KeepaliveMinTime = DefaultKeepaliveMinTime
	}
	if opts.ConnectionTimeout <= 0 {
		opts.ConnectionTimeout = DefaultConnectionTimeout
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.GracefulStopTimeout <= 0 {
		opts.GracefulStopTimeout = DefaultGracefulStopTimeout
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	creds, err := resolveCreds(opts)
	if err != nil {
		return nil, err
	}

	return &Transport{
		opts:   opts,
		log:    opts.Logger,
		creds:  creds,
		ready:  make(chan struct{}),
		closed: make(chan struct{}),
	}, nil
}

// MustNew is New, panicking on a configuration error.
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// resolveCreds picks the transport credentials, refusing to guess.
func resolveCreds(opts Options) (credentials.TransportCredentials, error) {
	switch {
	case opts.Creds != nil:
		return opts.Creds, nil
	case opts.TLSConfig != nil:
		return credentials.NewTLS(opts.TLSConfig), nil
	case opts.Insecure:
		return insecure.NewCredentials(), nil
	default:
		return nil, errors.New(
			"grpcmq: no transport security configured — set Options.Creds or Options.TLSConfig, " +
				"or set Options.Insecure to true to accept plaintext gRPC")
	}
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportGRPC }

// Addr returns the address the server is listening on, or nil before Listen has
// bound one. With a port of 0 the OS assigns a fresh port on every Listen, so the
// value changes across a supervisor restart; use a fixed port in production.
func (t *Transport) Addr() net.Addr {
	t.srvMu.Lock()
	defer t.srvMu.Unlock()
	if t.lis == nil {
		return nil
	}
	return t.lis.Addr()
}

// Ready closes once the server has bound its listener for the first time. It lets
// a caller — most often a test using port 0 — learn the address without polling.
func (t *Transport) Ready() <-chan struct{} { return t.ready }

// Close stops the server and the client connection. It is idempotent and unblocks
// every in-flight Request and Listen.
//
// It is the abrupt path: cancel the context passed to Listen for a graceful drain.
func (t *Transport) Close() error {
	var firstErr error

	t.closeOnce.Do(func() {
		// Released first so a waiter never depends on the teardown below.
		close(t.closed)

		t.srvMu.Lock()
		srv := t.srv
		t.srv = nil
		t.srvMu.Unlock()

		if srv != nil {
			// Stop, not GracefulStop: Close means stop now, and Listen's own
			// shutdown path already implements the graceful version.
			srv.Stop()
		}

		// clientOnce is tripped so a call racing Close cannot dial a fresh
		// connection that nobody will ever close.
		t.clientOnce.Do(func() {
			t.clientErr = microservice.ErrClosed
		})
		if t.clientConn != nil {
			if err := t.clientConn.Close(); err != nil {
				firstErr = fmt.Errorf("grpcmq: closing the client connection: %w", err)
			}
		}
	})

	return firstErr
}

func (t *Transport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// linkClose derives a context that is also cancelled when the transport closes,
// so a blocking RPC cannot survive Close. The helper goroutine ends with the
// returned cancel, so it cannot leak.
func (t *Transport) linkClose(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-ctx.Done():
		case <-t.closed:
			cancel()
		}
	}()
	return ctx, cancel
}

// mapTimeout normalises a deadline onto microservice.ErrTimeout.
func mapTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return microservice.ErrTimeout
	}
	return err
}
