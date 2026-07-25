package grpcmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/nika-framework/nika/common/microservice"
)

// Listen starts the gRPC server and blocks until ctx is cancelled or the server
// fails.
//
// patterns are not used for subscription — gRPC has no subjects, every call
// arrives on the same two methods — so the core Router does the matching. They are
// logged so a misconfiguration is visible.
func (t *Transport) Listen(ctx context.Context, patterns []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("grpcmq: Listen needs a dispatcher")
	}
	if t.opts.Addr == "" {
		return errors.New("grpcmq: Options.Addr is required to Listen")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	lis, err := net.Listen("tcp", t.opts.Addr)
	if err != nil {
		return fmt.Errorf("grpcmq: listen on %s: %w", t.opts.Addr, err)
	}

	srv := grpc.NewServer(t.serverOptions()...)
	srv.RegisterService(&serviceDesc, &server{
		t:          t,
		dispatcher: dispatch,
		slots:      newSlots(t.opts.Concurrency),
	})

	t.srvMu.Lock()
	if t.isClosed() {
		// Close ran between the guard above and here; do not leave a server and a
		// listener behind that nothing will ever stop.
		t.srvMu.Unlock()
		srv.Stop()
		_ = lis.Close()
		return microservice.ErrClosed
	}
	t.srv, t.lis = srv, lis
	t.srvMu.Unlock()
	t.readyOnce.Do(func() { close(t.ready) })

	t.log.Info("grpc server started",
		slog.String("addr", lis.Addr().String()),
		slog.String("service", ServiceName),
		slog.String("codec", CodecName),
		slog.Int("max_recv_bytes", t.opts.MaxRecvMsgSize),
		slog.Any("patterns", patterns))

	// Serve owns the listener and closes it when it returns.
	served := make(chan error, 1)
	go func() { served <- srv.Serve(lis) }()

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("grpcmq: serving on %s: %w", lis.Addr(), err)
		}
		return nil

	case <-t.closed:
		// Close already called Stop; just collect the result.
		<-served
		return microservice.ErrClosed

	case <-ctx.Done():
		t.stopGracefully(srv)
		<-served
		return nil
	}
}

// stopGracefully drains in-flight calls, then forces the issue.
//
// GracefulStop stops accepting connections and waits for every active RPC and
// stream to finish — with no timeout of its own. One client holding a
// bidirectional stream open, or one handler that never returns, blocks it forever,
// which turns a rolling deploy into a stuck pod. Waiting a bounded time and then
// calling Stop is the only shutdown that always terminates.
func (t *Transport) stopGracefully(srv *grpc.Server) {
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(t.opts.GracefulStopTimeout):
		t.log.Warn("grpcmq: graceful stop did not finish in time; forcing the server down",
			slog.Duration("waited", t.opts.GracefulStopTimeout))
		srv.Stop()
		<-stopped
	}
}

// serverOptions assembles the grpc.ServerOptions.
func (t *Transport) serverOptions() []grpc.ServerOption {
	opts := []grpc.ServerOption{
		grpc.Creds(t.creds),
		grpc.MaxRecvMsgSize(t.opts.MaxRecvMsgSize),
		// Symmetric on the send side: a reply can be as large as a request, and a
		// server that accepts an 8 MiB question but cannot send an 8 MiB answer
		// fails in a way that looks like the handler's fault.
		grpc.MaxSendMsgSize(t.opts.MaxRecvMsgSize),
		grpc.ConnectionTimeout(t.opts.ConnectionTimeout),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             t.opts.KeepaliveMinTime,
			PermitWithoutStream: t.opts.KeepalivePermitWithoutStream,
		}),
	}

	if t.opts.MaxConcurrentStreams > 0 {
		opts = append(opts, grpc.MaxConcurrentStreams(t.opts.MaxConcurrentStreams))
	}
	if len(t.opts.UnaryInterceptors) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(t.opts.UnaryInterceptors...))
	}
	if len(t.opts.StreamInterceptors) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(t.opts.StreamInterceptors...))
	}

	// No ForceServerCodec: the codec is selected per call by content-subtype, so a
	// protobuf service registered on the same grpc.Server by other code keeps
	// working. Forcing it would break every one of them.
	return append(opts, t.opts.ServerOptions...)
}

// newSlots builds the concurrency semaphore, or nil for no limit.
func newSlots(concurrency int) chan struct{} {
	if concurrency < 0 {
		return nil
	}
	return make(chan struct{}, concurrency)
}
