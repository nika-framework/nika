package grpcmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/nika-framework/nika/common/microservice"
)

// Publish sends an envelope and discards the reply.
//
// It is fire-and-forget only in the sense that the answer is thrown away. The call
// is still synchronous: the round trip happens, the handler runs, and an error is
// returned if the server refuses the message. And there is no store and forward —
// with the server down the message is lost, not queued. See the package comment.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("grpcmq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	// An empty ReplyTo is what tells the server to discard the handler's reply, so
	// the response frame is empty and no JSON is encoded on the way back.
	out := *env
	out.ReplyTo = ""

	body, err := out.Encode()
	if err != nil {
		return fmt.Errorf("grpcmq: encode envelope: %w", err)
	}

	// No explicit timeout: the caller's context is the only deadline a publish has,
	// and inventing one here would silently cap a legitimately slow handler.
	if _, err := t.invoke(ctx, body); err != nil {
		return err
	}
	return nil
}

// Request sends an envelope and returns the reply.
//
// gRPC is natively request/reply, so this is a plain unary call: no correlation
// map, no reply queue, no demultiplexing goroutine. The RPC itself is the
// correlation, which is why this transport is the cheapest request/reply of the
// set.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("grpcmq: cannot request with a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}

	if timeout <= 0 {
		timeout = t.opts.ReplyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := *env
	if out.ID == "" {
		out.ID = microservice.NewID()
	}
	out.ReplyTo = replyInline

	body, err := out.Encode()
	if err != nil {
		return nil, fmt.Errorf("grpcmq: encode envelope: %w", err)
	}

	replyBody, err := t.invoke(ctx, body)
	if err != nil {
		return nil, err
	}

	reply, err := microservice.DecodeEnvelope(replyBody)
	if err != nil {
		return nil, fmt.Errorf("grpcmq: malformed reply from %s: %w", t.opts.Target, err)
	}
	return reply, nil
}

// invoke performs the unary call.
func (t *Transport) invoke(ctx context.Context, body []byte) ([]byte, error) {
	conn, err := t.client()
	if err != nil {
		return nil, err
	}

	// Cancelling on Close is what lets Close unblock a call that is waiting on a
	// server which will never answer.
	ctx, cancel := t.linkClose(ctx)
	defer cancel()

	reply := new(rawMessage)
	err = conn.Invoke(ctx, MethodDispatch, &rawMessage{body: body}, reply, t.callOptions()...)
	if err != nil {
		if t.isClosed() {
			return nil, microservice.ErrClosed
		}
		return nil, fmt.Errorf("grpcmq: calling %s: %w", t.opts.Target, mapTimeout(err))
	}
	return reply.body, nil
}

// callOptions selects the pass-through codec and the readiness policy.
//
// CallContentSubtype is the whole trick: it sets the request's content-type to
// "application/grpc+nika-raw", which makes both ends look the codec up by that
// name in the global registry — where this package's init put it.
func (t *Transport) callOptions() []grpc.CallOption {
	return []grpc.CallOption{
		grpc.CallContentSubtype(CodecName),
		grpc.WaitForReady(t.opts.WaitForReady),
		grpc.MaxCallRecvMsgSize(t.opts.MaxRecvMsgSize),
		grpc.MaxCallSendMsgSize(t.opts.MaxRecvMsgSize),
	}
}

// client returns the shared client connection, building it once.
func (t *Transport) client() (*grpc.ClientConn, error) {
	t.clientOnce.Do(func() {
		if t.opts.Target == "" {
			t.clientErr = errors.New("grpcmq: Options.Target is required to publish or request")
			return
		}
		// grpc.NewClient, not the deprecated grpc.Dial: it never blocks, so a
		// server that is briefly down does not stop this process from starting, and
		// connection management is left to grpc-go's own backoff.
		conn, err := grpc.NewClient(t.opts.Target, t.dialOptions()...)
		if err != nil {
			t.clientErr = fmt.Errorf("grpcmq: creating a client for %s: %w", t.opts.Target, err)
			return
		}
		t.clientConn = conn
	})
	if t.clientErr != nil {
		return nil, t.clientErr
	}
	// Close trips clientOnce with ErrClosed, so a nil connection here means Close
	// won the race.
	if t.clientConn == nil {
		return nil, microservice.ErrClosed
	}
	return t.clientConn, nil
}

// dialOptions assembles the grpc.DialOptions.
func (t *Transport) dialOptions() []grpc.DialOption {
	var keepaliveParams keepalive.ClientParameters = t.opts.ClientKeepalive
	if keepaliveParams.Timeout == 0 {
		keepaliveParams.Timeout = t.opts.DialTimeout
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(t.creds),
		grpc.WithDefaultCallOptions(t.callOptions()...),
	}
	if keepaliveParams.Time > 0 {
		// Only set when asked: a Time below the server's KeepaliveMinTime gets the
		// connection dropped with ENHANCE_YOUR_CALM, so a default here would be a
		// trap.
		opts = append(opts, grpc.WithKeepaliveParams(keepaliveParams))
	}

	return append(opts, t.opts.DialOptions...)
}
