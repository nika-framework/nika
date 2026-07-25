package grpcmq

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nika-framework/nika/common/microservice"
)

// The service is described by hand instead of being generated from a .proto file.
//
// A grpc.ServiceDesc is just data: a fully-qualified service name, the method
// names, and a function per method that knows how to decode a request, call the
// implementation and hand back a response. protoc produces exactly this struct
// and nothing magic; writing it out is a few dozen lines and removes the codegen
// step, the generated files from review, and the risk of the .pb.go drifting from
// the envelope definition it is supposed to mirror.
//
// The method paths are the normal gRPC ones, so grpcurl, a service mesh, an
// interceptor or an access log sees an ordinary service.
const (
	// ServiceName is the fully-qualified gRPC service name.
	ServiceName = "nika.microservice.v1.Messenger"

	// MethodDispatch is the unary method: one envelope in, one envelope out.
	MethodDispatch = "/" + ServiceName + "/Dispatch"

	// MethodStream is the bidirectional method: many envelopes over one
	// connection, replies in the order the requests arrived.
	MethodStream = "/" + ServiceName + "/Stream"
)

// replyInline is the ReplyTo value Request sets.
//
// Every other transport puts a broker address in ReplyTo. gRPC has no such
// address — the reply travels back down the RPC that carried the request — so a
// marker is used instead. It still carries the meaning the rest of the framework
// relies on: empty ReplyTo means fire-and-forget and the handler's reply is
// discarded, non-empty means the caller is waiting for it.
const replyInline = "grpc"

// messenger is the server-side interface behind the ServiceDesc. It is unexported
// because the only implementation is in this package; gRPC uses it purely to
// check at registration time that the implementation matches the description.
type messenger interface {
	dispatch(ctx context.Context, in *rawMessage) (*rawMessage, error)
	stream(stream grpc.ServerStream) error
}

// serviceDesc is the hand-written equivalent of a generated _ServiceDesc.
var serviceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*messenger)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Dispatch", Handler: dispatchHandler},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Stream",
			Handler:       streamHandler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "nika/microservice/v1/messenger.nika-raw",
}

// dispatchHandler is the generated-code shape for a unary method: decode into a
// fresh message, then either call the implementation or hand it to the
// interceptor chain.
func dispatchHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(rawMessage)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(messenger).dispatch(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: MethodDispatch}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(messenger).dispatch(ctx, req.(*rawMessage))
	}
	return interceptor(ctx, in, info, handler)
}

// streamHandler is the generated-code shape for a streaming method. Stream
// interceptors are applied by grpc-go around this call, so nothing is needed here.
func streamHandler(srv any, stream grpc.ServerStream) error {
	return srv.(messenger).stream(stream)
}

// server implements messenger for one Listen call.
type server struct {
	t          *Transport
	dispatcher microservice.Dispatcher

	// slots bounds concurrent handlers. grpc-go runs every RPC in its own
	// goroutine, and MaxConcurrentStreams is per connection, so N clients can
	// still produce N × MaxConcurrentStreams handlers at once. This is the
	// process-wide backstop.
	slots chan struct{}
}

var _ messenger = (*server)(nil)

// dispatch handles one unary RPC.
func (s *server) dispatch(ctx context.Context, in *rawMessage) (*rawMessage, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	env, err := microservice.DecodeEnvelope(in.body)
	if err != nil {
		// A unary RPC is per-message, so failing it affects only this call. The
		// status code matters: InvalidArgument tells the caller not to retry, which
		// is correct — the same bytes will fail again.
		s.t.log.Warn("grpcmq: rejecting a malformed envelope",
			slog.Int("bytes", len(in.body)), slog.Any("error", err))
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	reply, err := s.dispatcher(ctx, env)
	if err != nil {
		// Per the Dispatcher contract, an error means the message itself was
		// unusable rather than that the handler failed — a handler failure arrives
		// as a reply envelope carrying an EnvelopeError.
		s.t.log.Warn("grpcmq: dispatch rejected the message",
			slog.String("pattern", env.Pattern), slog.Any("error", err))
		return nil, status.Error(codes.Internal, err.Error())
	}

	if env.ReplyTo == "" {
		// Fire-and-forget: the reply is discarded and the caller gets an empty
		// frame. It still costs a round trip; see Publish.
		return &rawMessage{body: []byte{}}, nil
	}

	body, err := encodeReply(env, reply)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &rawMessage{body: body}, nil
}

// stream handles one bidirectional stream, answering each request in turn.
//
// Messages are handled one at a time on purpose. A gRPC stream delivers frames in
// order, so replying in order lets the client correlate by position and needs no
// correlation map at all; handling them concurrently would reorder the replies and
// require one. Open several streams — or use the unary method, which grpc-go
// multiplexes over the same connection anyway — for concurrency.
func (s *server) stream(stream grpc.ServerStream) error {
	ctx := stream.Context()

	for {
		in := new(rawMessage)
		if err := stream.RecvMsg(in); err != nil {
			if errors.Is(err, io.EOF) {
				// The client closed its send side: a clean end of stream.
				return nil
			}
			return err
		}

		out, err := s.handleStreamed(ctx, in)
		if err != nil {
			return err
		}
		if out == nil {
			continue
		}
		if err := stream.SendMsg(out); err != nil {
			return err
		}
	}
}

// handleStreamed is stream's per-message body. A malformed message is answered
// with an error envelope rather than by failing the stream: a stream carries many
// messages, and killing it over one bad frame would take down every other
// in-flight message on it.
func (s *server) handleStreamed(ctx context.Context, in *rawMessage) (*rawMessage, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	env, err := microservice.DecodeEnvelope(in.body)
	if err != nil {
		s.t.log.Warn("grpcmq: skipping a malformed envelope on a stream",
			slog.Int("bytes", len(in.body)), slog.Any("error", err))
		body, encErr := (&microservice.Envelope{
			Pattern: "unknown",
			Status:  400,
			Error: &microservice.EnvelopeError{
				Code:    400,
				Message: "MALFORMED_ENVELOPE",
				Details: err.Error(),
			},
		}).Encode()
		if encErr != nil {
			return nil, status.Error(codes.Internal, encErr.Error())
		}
		return &rawMessage{body: body}, nil
	}

	reply, err := s.dispatcher(ctx, env)
	if err != nil {
		s.t.log.Warn("grpcmq: dispatch rejected a streamed message",
			slog.String("pattern", env.Pattern), slog.Any("error", err))
		reply = nil
	}

	if env.ReplyTo == "" {
		// Fire-and-forget on a stream sends nothing back, so a publisher can pump
		// events through one stream without a reply frame per event.
		return nil, nil
	}

	body, err := encodeReply(env, reply)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &rawMessage{body: body}, nil
}

// acquire takes a concurrency slot, returning ResourceExhausted rather than
// queueing forever when the server is saturated and the caller's deadline passes.
func (s *server) acquire(ctx context.Context) (func(), error) {
	if s.slots == nil {
		return func() {}, nil
	}
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	case <-ctx.Done():
		return nil, status.Error(codes.ResourceExhausted,
			"grpcmq: the server is at its concurrency limit")
	}
}

// encodeReply serialises a handler's reply, substituting an error envelope when
// the handler produced none. The reply always carries the request's id, so the
// caller can correlate even when the reply travelled over a stream.
func encodeReply(env *microservice.Envelope, reply *microservice.Envelope) ([]byte, error) {
	if reply == nil {
		reply = &microservice.Envelope{
			Pattern: env.Pattern,
			Status:  500,
			Error: &microservice.EnvelopeError{
				Code:    500,
				Message: "DISPATCH_ERROR",
				Details: "handler produced no reply",
			},
		}
	}
	reply.ID = env.ID
	return reply.Encode()
}
