package grpcmq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nika-framework/nika/common/microservice"
)

// testTimeout bounds every test so a regression that deadlocks fails the run
// instead of stalling it. gRPC needs no broker, so these are real end-to-end
// tests over a loopback socket.
const testTimeout = 5 * time.Second

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// serve starts a listener on an OS-assigned loopback port and returns the address
// it bound. Teardown cancels Listen and waits for it, so a leaked goroutine or a
// shutdown that never returns fails the test.
func serve(t *testing.T, opts Options, dispatch microservice.Dispatcher) (*Transport, string) {
	t.Helper()

	opts.Addr = "127.0.0.1:0"
	opts.Insecure = true
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New(server): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Listen(ctx, []string{"echo", "fail_me", "big"}, dispatch) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, microservice.ErrClosed) {
				t.Errorf("Listen returned %v", err)
			}
		case <-time.After(testTimeout):
			t.Error("Listen did not return after its context was cancelled")
		}
		_ = server.Close()
	})

	select {
	case <-server.Ready():
	case <-time.After(testTimeout):
		t.Fatal("the server never became ready")
	}

	addr := server.Addr()
	if addr == nil {
		t.Fatal("Addr() is nil after Ready()")
	}
	return server, addr.String()
}

// dial builds a client transport pointed at target.
func dial(t *testing.T, target string, mutate func(*Options)) *Transport {
	t.Helper()

	opts := Options{Target: target, Insecure: true}
	if mutate != nil {
		mutate(&opts)
	}
	client, err := New(opts)
	if err != nil {
		t.Fatalf("New(client): %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// echoDispatcher answers "echo" with the request payload, "fail_me" with a
// handler-level failure encoded in the envelope, and "big" with a large payload.
func echoDispatcher(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	switch env.Pattern {
	case "fail_me":
		return &microservice.Envelope{
			ID:      env.ID,
			Pattern: env.Pattern,
			Status:  422,
			Error: &microservice.EnvelopeError{
				Code:    422,
				Message: "VALIDATION_FAILED",
				Details: "name is required",
			},
		}, nil
	default:
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200, Data: env.Data}, nil
	}
}

// --- codec ---

func TestCodecRoundTripsArbitraryBytes(t *testing.T) {
	codec := rawCodec{}

	cases := map[string][]byte{
		"empty":              {},
		"nil":                nil,
		"json":               []byte(`{"id":"1","pattern":"echo"}`),
		"invalid utf8":       {0xff, 0xfe, 0x00, 0x80, 0x41},
		"nul bytes":          {0x00, 0x00, 0x00},
		"newlines and quote": []byte("line\n\"quoted\"\r\n"),
		"large":              bytes.Repeat([]byte{0xab}, 1<<20),
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			wire, err := codec.Marshal(&rawMessage{body: payload})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !bytes.Equal(wire, payload) {
				t.Fatalf("Marshal changed the payload: %x != %x", wire, payload)
			}

			var out rawMessage
			if err := codec.Unmarshal(wire, &out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !bytes.Equal(out.body, payload) {
				t.Fatalf("round trip = %x, want %x", out.body, payload)
			}
			if len(payload) == 0 && out.body == nil {
				t.Fatal("an empty frame should decode to an empty, non-nil body")
			}
		})
	}
}

func TestCodecCopiesTheFrame(t *testing.T) {
	// gRPC frees the buffer as soon as Unmarshal returns, so retaining it would
	// hand the decoder recycled memory.
	frame := []byte(`{"id":"1"}`)
	var out rawMessage
	if err := (rawCodec{}).Unmarshal(frame, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	frame[2] = 'X'
	if bytes.Equal(out.body, frame) {
		t.Fatal("Unmarshal aliased the caller's buffer instead of copying it")
	}
}

func TestCodecRejectsForeignTypes(t *testing.T) {
	codec := rawCodec{}
	if _, err := codec.Marshal("a string"); err == nil {
		t.Error("Marshal should reject a type it does not own")
	}
	if err := codec.Unmarshal([]byte("x"), new(string)); err == nil {
		t.Error("Unmarshal should reject a type it does not own")
	}
	if codec.Name() != CodecName {
		t.Errorf("Name() = %q, want %q", codec.Name(), CodecName)
	}
	if got, err := codec.Marshal((*rawMessage)(nil)); err != nil || got != nil {
		t.Errorf("Marshal(nil message) = (%v, %v), want (nil, nil)", got, err)
	}
}

// --- options ---

func TestNewRequiresAnAddrOrTarget(t *testing.T) {
	if _, err := New(Options{Insecure: true}); err == nil {
		t.Fatal("New with neither Addr nor Target should fail")
	}
}

func TestNewRequiresExplicitTransportSecurity(t *testing.T) {
	_, err := New(Options{Target: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("New without credentials and without Insecure should fail")
	}
	if !strings.Contains(err.Error(), "Insecure") {
		t.Fatalf("the error should point at the opt-in, got %v", err)
	}

	if _, err := New(Options{Target: "127.0.0.1:1", Insecure: true}); err != nil {
		t.Fatalf("Insecure:true should be accepted: %v", err)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr, err := New(Options{Addr: "127.0.0.1:0", Target: "127.0.0.1:1", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if tr.opts.MaxRecvMsgSize != DefaultMaxRecvMsgSize {
		t.Errorf("MaxRecvMsgSize = %d, want %d (gRPC's own 4 MiB default rejects larger envelopes)",
			tr.opts.MaxRecvMsgSize, DefaultMaxRecvMsgSize)
	}
	if tr.opts.KeepaliveMinTime != DefaultKeepaliveMinTime {
		t.Errorf("KeepaliveMinTime = %v, want %v", tr.opts.KeepaliveMinTime, DefaultKeepaliveMinTime)
	}
	if tr.opts.KeepalivePermitWithoutStream {
		t.Error("KeepalivePermitWithoutStream should default to false")
	}
	if tr.opts.ConnectionTimeout != DefaultConnectionTimeout {
		t.Errorf("ConnectionTimeout = %v, want %v", tr.opts.ConnectionTimeout, DefaultConnectionTimeout)
	}
	if tr.opts.Concurrency != DefaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", tr.opts.Concurrency, DefaultConcurrency)
	}
	if tr.opts.GracefulStopTimeout != DefaultGracefulStopTimeout {
		t.Errorf("GracefulStopTimeout = %v, want %v", tr.opts.GracefulStopTimeout, DefaultGracefulStopTimeout)
	}
	if tr.opts.DialTimeout != DefaultDialTimeout {
		t.Errorf("DialTimeout = %v, want %v", tr.opts.DialTimeout, DefaultDialTimeout)
	}
	if tr.opts.ReplyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("ReplyTimeout = %v, want %v", tr.opts.ReplyTimeout, microservice.DefaultRequestTimeout)
	}
	if tr.opts.WaitForReady {
		t.Error("WaitForReady should default to false so a dead server fails fast")
	}
	if tr.creds == nil {
		t.Error("credentials should be resolved in New")
	}
	if tr.opts.Logger == nil {
		t.Error("Logger should default to slog.Default()")
	}
	if tr.Addr() != nil {
		t.Error("Addr() should be nil before Listen binds")
	}
}

func TestMustNewPanicsOnBadOptions(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew should panic when Options are invalid")
		}
	}()
	MustNew(Options{})
}

func TestName(t *testing.T) {
	tr := dial(t, "127.0.0.1:1", nil)
	if got := tr.Name(); got != microservice.TransportGRPC {
		t.Fatalf("Name() = %q, want %q", got, microservice.TransportGRPC)
	}
}

func TestNewSlots(t *testing.T) {
	if got := newSlots(-1); got != nil {
		t.Error("a negative concurrency should mean no limit")
	}
	if got := cap(newSlots(4)); got != 4 {
		t.Errorf("cap(newSlots(4)) = %d, want 4", got)
	}
}

// --- request/reply ---

func TestRequestReply(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	env, err := microservice.NewEnvelope("echo", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	reply, err := client.Request(testContext(t), env, 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID != env.ID {
		t.Fatalf("reply id = %q, want %q", reply.ID, env.ID)
	}
	if reply.Status != 200 {
		t.Fatalf("reply status = %d, want 200", reply.Status)
	}
	var payload map[string]string
	if err := json.Unmarshal(reply.Data, &payload); err != nil {
		t.Fatalf("reply payload: %v", err)
	}
	if payload["hello"] != "world" {
		t.Fatalf("reply payload = %v", payload)
	}
}

func TestRequestGeneratesAnIDWhenMissing(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	reply, err := client.Request(testContext(t), &microservice.Envelope{Pattern: "echo"}, 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID == "" {
		t.Fatal("Request should generate a correlation id when the caller left it empty")
	}
}

func TestRequestDoesNotMutateTheCallersEnvelope(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	env := &microservice.Envelope{ID: "fixed-id", Pattern: "echo"}
	if _, err := client.Request(testContext(t), env, 2*time.Second); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if env.ReplyTo != "" {
		t.Fatalf("Request set ReplyTo on the caller's envelope: %q", env.ReplyTo)
	}
}

func TestHandlerErrorArrivesAsAnEnvelopeError(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	env, _ := microservice.NewEnvelope("fail_me", nil)
	reply, err := client.Request(testContext(t), env, 2*time.Second)
	if err != nil {
		t.Fatalf("a handler failure must not become a transport error, got %v", err)
	}
	if reply.Error == nil {
		t.Fatal("the reply should carry an EnvelopeError")
	}
	if reply.Status != 422 || reply.Error.Code != 422 || reply.Error.Message != "VALIDATION_FAILED" {
		t.Fatalf("reply = %+v / %+v", reply, reply.Error)
	}
	if reply.ID != env.ID {
		t.Fatalf("reply id = %q, want %q", reply.ID, env.ID)
	}
}

func TestDispatcherErrorBecomesAStatusError(t *testing.T) {
	// A Dispatcher error means the message itself was unusable, which must reach
	// the caller as a failed RPC rather than as a silent empty reply.
	_, addr := serve(t, Options{}, func(_ context.Context, _ *microservice.Envelope) (*microservice.Envelope, error) {
		return nil, errors.New("unusable message")
	})
	client := dial(t, addr, nil)

	env, _ := microservice.NewEnvelope("echo", nil)
	_, err := client.Request(testContext(t), env, 2*time.Second)
	if err == nil {
		t.Fatal("Request should fail when the dispatcher rejects the message")
	}
	if got := status.Code(errors.Unwrap(err)); got != codes.Internal {
		t.Fatalf("status code = %v, want Internal (err: %v)", got, err)
	}
}

func TestHandlerWithNoReplyStillAnswers(t *testing.T) {
	_, addr := serve(t, Options{}, func(_ context.Context, _ *microservice.Envelope) (*microservice.Envelope, error) {
		return nil, nil
	})
	client := dial(t, addr, nil)

	env, _ := microservice.NewEnvelope("echo", nil)
	reply, err := client.Request(testContext(t), env, 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.Error == nil || reply.Status != 500 {
		t.Fatalf("a handler that produced nothing should yield a 500 envelope, got %+v", reply)
	}
}

func TestMalformedEnvelopeIsRejectedWithInvalidArgument(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	// Bypass Request so the bytes on the wire are genuinely not an envelope.
	_, err := client.invoke(testContext(t), []byte("{ this is not an envelope"))
	if err == nil {
		t.Fatal("a malformed envelope should be rejected")
	}
	if got := status.Code(errors.Unwrap(err)); got != codes.InvalidArgument {
		t.Fatalf("status code = %v, want InvalidArgument (err: %v)", got, err)
	}

	// The server must still be serving: a bad message may not kill it.
	env, _ := microservice.NewEnvelope("echo", nil)
	if _, err := client.Request(testContext(t), env, 2*time.Second); err != nil {
		t.Fatalf("the server stopped serving after a malformed message: %v", err)
	}
}

// --- publish ---

func TestPublishDiscardsTheReply(t *testing.T) {
	seen := make(chan *microservice.Envelope, 1)
	_, addr := serve(t, Options{}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		seen <- env
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})
	client := dial(t, addr, nil)

	env, _ := microservice.NewEnvelope("echo", map[string]int{"n": 1})
	env.ReplyTo = "should-be-cleared"
	if err := client.Publish(testContext(t), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-seen:
		if got.ReplyTo != "" {
			t.Fatalf("the server saw ReplyTo %q; Publish must clear it so the reply is discarded", got.ReplyTo)
		}
	case <-time.After(testTimeout):
		t.Fatal("the published message never reached the handler")
	}

	if env.ReplyTo != "should-be-cleared" {
		t.Fatal("Publish must not mutate the caller's envelope")
	}
}

// --- limits ---

func TestPayloadOverMaxRecvMsgSizeIsRejectedCleanly(t *testing.T) {
	const limit = 64 << 10
	_, addr := serve(t, Options{MaxRecvMsgSize: limit}, echoDispatcher)
	// The client's send limit must be generous enough to let the server be the one
	// that refuses, which is the behaviour under test.
	client := dial(t, addr, func(o *Options) { o.MaxRecvMsgSize = 8 << 20 })

	oversized := strings.Repeat("x", limit*2)
	env, err := microservice.NewEnvelope("big", map[string]string{"blob": oversized})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	_, err = client.Request(testContext(t), env, 3*time.Second)
	if err == nil {
		t.Fatal("a payload over MaxRecvMsgSize should be rejected")
	}
	if got := status.Code(errors.Unwrap(err)); got != codes.ResourceExhausted {
		t.Fatalf("status code = %v, want ResourceExhausted (err: %v)", got, err)
	}

	// And the connection must survive it.
	small, _ := microservice.NewEnvelope("echo", map[string]int{"n": 1})
	if _, err := client.Request(testContext(t), small, 3*time.Second); err != nil {
		t.Fatalf("the connection did not survive an oversized message: %v", err)
	}
}

func TestPayloadUnderTheLimitIsAccepted(t *testing.T) {
	const limit = 1 << 20
	_, addr := serve(t, Options{MaxRecvMsgSize: limit}, echoDispatcher)
	client := dial(t, addr, func(o *Options) { o.MaxRecvMsgSize = limit })

	env, _ := microservice.NewEnvelope("big", map[string]string{"blob": strings.Repeat("y", 256<<10)})
	if _, err := client.Request(testContext(t), env, 3*time.Second); err != nil {
		t.Fatalf("a payload inside the limit should be accepted: %v", err)
	}
}

// --- concurrency ---

func TestConcurrentRequests(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	ctx := testContext(t)
	const workers = 32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env, err := microservice.NewEnvelope("echo", map[string]int{"n": i})
			if err != nil {
				t.Errorf("NewEnvelope: %v", err)
				return
			}
			reply, err := client.Request(ctx, env, 4*time.Second)
			if err != nil {
				t.Errorf("Request %d: %v", i, err)
				return
			}
			if reply.ID != env.ID {
				t.Errorf("request %d got the reply for %q", i, reply.ID)
				return
			}
			var payload map[string]int
			if err := json.Unmarshal(reply.Data, &payload); err != nil || payload["n"] != i {
				t.Errorf("request %d got payload %s (%v)", i, reply.Data, err)
			}
		}(i)
	}
	wg.Wait()
}

// --- lifecycle ---

func TestCloseIsIdempotent(t *testing.T) {
	tr := dial(t, "127.0.0.1:1", nil)
	for i := 0; i < 3; i++ {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
	if !tr.isClosed() {
		t.Fatal("transport should report itself closed")
	}
}

func TestCloseIsIdempotentUnderConcurrency(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	// Make sure a real connection exists before racing Close against itself.
	env, _ := microservice.NewEnvelope("echo", nil)
	if _, err := client.Request(testContext(t), env, 2*time.Second); err != nil {
		t.Fatalf("Request: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr := dial(t, "127.0.0.1:1", func(o *Options) { o.Addr = "127.0.0.1:0" })
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := testContext(t)
	env := &microservice.Envelope{ID: microservice.NewID(), Pattern: "echo"}

	if err := tr.Publish(ctx, env); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if _, err := tr.Request(ctx, env, time.Second); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	if err := tr.Listen(ctx, []string{"echo"}, echoDispatcher); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Listen after Close = %v, want ErrClosed", err)
	}
}

func TestCloseUnblocksAPendingRequest(t *testing.T) {
	// The handler never returns, so only Close can end the call.
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	_, addr := serve(t, Options{}, func(ctx context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		select {
		case <-released:
		case <-ctx.Done():
		}
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := dial(t, addr, nil)

	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(entered)
		env, _ := microservice.NewEnvelope("echo", nil)
		// A generous timeout: if Close does not unblock this, the test must fail on
		// its own deadline rather than pass because the request timed out.
		_, err := client.Request(context.Background(), env, time.Minute)
		done <- err
	}()

	<-entered
	time.Sleep(150 * time.Millisecond) // let the RPC actually reach the server

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, microservice.ErrClosed) {
			t.Fatalf("Request after Close = %v, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock the pending Request")
	}
}

func TestRequestTimesOut(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })

	_, addr := serve(t, Options{}, func(ctx context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern}, nil
	})
	client := dial(t, addr, nil)

	env, _ := microservice.NewEnvelope("echo", nil)
	_, err := client.Request(context.Background(), env, 150*time.Millisecond)
	if err == nil {
		t.Fatal("Request should have timed out")
	}
	// gRPC turns the deadline into a DeadlineExceeded status, which is the same
	// condition ErrTimeout names.
	if code := status.Code(errors.Unwrap(err)); code != codes.DeadlineExceeded && !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want a timeout", err)
	}
}

func TestGracefulStopDrainsAnInFlightCall(t *testing.T) {
	started := make(chan struct{}, 1)

	server, err := New(Options{Addr: "127.0.0.1:0", Insecure: true, GracefulStopTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("New(server): %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Listen(ctx, []string{"echo"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
			started <- struct{}{}
			// Long enough that the graceful stop below genuinely has to wait.
			time.Sleep(400 * time.Millisecond)
			return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
		})
	}()

	select {
	case <-server.Ready():
	case <-time.After(testTimeout):
		t.Fatal("the server never became ready")
	}

	client := dial(t, server.Addr().String(), nil)

	reply := make(chan *microservice.Envelope, 1)
	callErr := make(chan error, 1)
	env, _ := microservice.NewEnvelope("echo", nil)
	go func() {
		got, err := client.Request(context.Background(), env, 10*time.Second)
		if err != nil {
			callErr <- err
			return
		}
		reply <- got
	}()

	// Cancel Listen while the handler is mid-flight; GracefulStop must let it
	// finish and let the reply reach the client.
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("the handler never started")
	}
	cancel()

	select {
	case got := <-reply:
		if got.ID != env.ID || got.Status != 200 {
			t.Fatalf("drained reply = %+v", got)
		}
	case err := <-callErr:
		t.Fatalf("the in-flight call was not drained: %v", err)
	case <-time.After(testTimeout):
		t.Fatal("no reply and no error from the in-flight call")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Listen returned %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Listen did not return after a graceful stop")
	}
}

func TestListenRejectsBadArguments(t *testing.T) {
	tr, err := New(Options{Addr: "127.0.0.1:0", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if err := tr.Listen(testContext(t), nil, nil); err == nil {
		t.Error("Listen without a dispatcher should fail")
	}

	clientOnly := dial(t, "127.0.0.1:1", nil)
	if err := clientOnly.Listen(testContext(t), nil, echoDispatcher); err == nil {
		t.Error("Listen without an Addr should fail")
	}
}

func TestListenFailsOnAnAddressInUse(t *testing.T) {
	server, addr := serve(t, Options{}, echoDispatcher)
	_ = server

	second, err := New(Options{Addr: addr, Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if err := second.Listen(testContext(t), []string{"echo"}, echoDispatcher); err == nil {
		t.Fatal("Listen on an occupied port should fail so the supervisor can report it")
	}
}

func TestClientDialFailureIsReported(t *testing.T) {
	// Nothing is listening on this port, and WaitForReady is off, so the call must
	// fail fast rather than hang until its deadline.
	client := dial(t, "127.0.0.1:1", nil)

	env, _ := microservice.NewEnvelope("echo", nil)
	start := time.Now()
	_, err := client.Request(context.Background(), env, 3*time.Second)
	if err == nil {
		t.Fatal("Request against a dead target should fail")
	}
	if code := status.Code(errors.Unwrap(err)); code != codes.Unavailable {
		t.Fatalf("status code = %v, want Unavailable (err: %v)", code, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the call took %v; without WaitForReady it should fail fast", elapsed)
	}
}

func TestPublishAndRequestNeedATarget(t *testing.T) {
	serverOnly, err := New(Options{Addr: "127.0.0.1:0", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = serverOnly.Close() })

	ctx := testContext(t)
	env := &microservice.Envelope{ID: "1", Pattern: "echo"}

	if err := serverOnly.Publish(ctx, env); err == nil || !strings.Contains(err.Error(), "Target") {
		t.Errorf("Publish without a Target = %v, want an error naming Target", err)
	}
	if _, err := serverOnly.Request(ctx, env, time.Second); err == nil || !strings.Contains(err.Error(), "Target") {
		t.Errorf("Request without a Target = %v, want an error naming Target", err)
	}
}

func TestNilEnvelopeIsRejected(t *testing.T) {
	tr := dial(t, "127.0.0.1:1", nil)
	ctx := testContext(t)

	if err := tr.Publish(ctx, nil); err == nil {
		t.Error("Publish(nil) should fail")
	}
	if _, err := tr.Request(ctx, nil, time.Second); err == nil {
		t.Error("Request(nil) should fail")
	}
}

// --- streaming ---

func TestBidirectionalStream(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	conn, err := client.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := testContext(t)
	stream, err := conn.NewStream(ctx, &serviceDesc.Streams[0], MethodStream, client.callOptions()...)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}

	const count = 8
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		env, err := microservice.NewEnvelope("echo", map[string]int{"n": i})
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		env.ReplyTo = replyInline
		ids[i] = env.ID

		body, err := env.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if err := stream.SendMsg(&rawMessage{body: body}); err != nil {
			t.Fatalf("SendMsg %d: %v", i, err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// Replies come back in request order, which is the guarantee the server's
	// serial handling buys.
	for i := 0; i < count; i++ {
		out := new(rawMessage)
		if err := stream.RecvMsg(out); err != nil {
			t.Fatalf("RecvMsg %d: %v", i, err)
		}
		reply, err := microservice.DecodeEnvelope(out.body)
		if err != nil {
			t.Fatalf("decode reply %d: %v", i, err)
		}
		if reply.ID != ids[i] {
			t.Fatalf("reply %d has id %q, want %q — the stream reordered replies", i, reply.ID, ids[i])
		}
	}
}

func TestStreamSkipsAMalformedMessageWithoutDying(t *testing.T) {
	_, addr := serve(t, Options{}, echoDispatcher)
	client := dial(t, addr, nil)

	conn, err := client.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := testContext(t)
	stream, err := conn.NewStream(ctx, &serviceDesc.Streams[0], MethodStream, client.callOptions()...)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}

	if err := stream.SendMsg(&rawMessage{body: []byte("{ not an envelope")}); err != nil {
		t.Fatalf("SendMsg(garbage): %v", err)
	}

	bad := new(rawMessage)
	if err := stream.RecvMsg(bad); err != nil {
		t.Fatalf("the stream died on a malformed message: %v", err)
	}
	badReply, err := microservice.DecodeEnvelope(bad.body)
	if err != nil {
		t.Fatalf("decode error reply: %v", err)
	}
	if badReply.Error == nil || badReply.Status != 400 {
		t.Fatalf("want a 400 error envelope, got %+v", badReply)
	}

	// The stream must still carry good traffic afterwards.
	env, _ := microservice.NewEnvelope("echo", nil)
	env.ReplyTo = replyInline
	body, _ := env.Encode()
	if err := stream.SendMsg(&rawMessage{body: body}); err != nil {
		t.Fatalf("SendMsg after garbage: %v", err)
	}
	good := new(rawMessage)
	if err := stream.RecvMsg(good); err != nil {
		t.Fatalf("RecvMsg after garbage: %v", err)
	}
	goodReply, err := microservice.DecodeEnvelope(good.body)
	if err != nil {
		t.Fatalf("decode good reply: %v", err)
	}
	if goodReply.ID != env.ID {
		t.Fatalf("reply id = %q, want %q", goodReply.ID, env.ID)
	}
}

func TestStreamFireAndForgetSendsNothingBack(t *testing.T) {
	seen := make(chan struct{}, 4)
	_, addr := serve(t, Options{}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		seen <- struct{}{}
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})
	client := dial(t, addr, nil)

	conn, err := client.client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := testContext(t)
	stream, err := conn.NewStream(ctx, &serviceDesc.Streams[0], MethodStream, client.callOptions()...)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}

	// No ReplyTo: an event, so the server must not send a reply frame.
	env, _ := microservice.NewEnvelope("echo", nil)
	body, _ := env.Encode()
	if err := stream.SendMsg(&rawMessage{body: body}); err != nil {
		t.Fatalf("SendMsg: %v", err)
	}

	select {
	case <-seen:
	case <-time.After(testTimeout):
		t.Fatal("the streamed event never reached the handler")
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
	out := new(rawMessage)
	if err := stream.RecvMsg(out); err == nil {
		t.Fatalf("a fire-and-forget stream message should produce no reply, got %q", out.body)
	}
}

// --- helpers ---

func TestMapTimeout(t *testing.T) {
	if got := mapTimeout(context.DeadlineExceeded); !errors.Is(got, microservice.ErrTimeout) {
		t.Errorf("mapTimeout(DeadlineExceeded) = %v, want ErrTimeout", got)
	}
	if got := mapTimeout(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("mapTimeout(Canceled) = %v, want it passed through", got)
	}
	if got := mapTimeout(nil); got != nil {
		t.Errorf("mapTimeout(nil) = %v, want nil", got)
	}
}

func TestLinkCloseCancelsOnTransportClose(t *testing.T) {
	tr := dial(t, "127.0.0.1:1", nil)

	ctx, cancel := tr.linkClose(context.Background())
	defer cancel()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(testTimeout):
		t.Fatal("linkClose did not cancel when the transport closed")
	}
}

func TestEncodeReplySubstitutesAnErrorEnvelope(t *testing.T) {
	env := &microservice.Envelope{ID: "abc", Pattern: "echo"}

	body, err := encodeReply(env, nil)
	if err != nil {
		t.Fatalf("encodeReply: %v", err)
	}
	reply, err := microservice.DecodeEnvelope(body)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if reply.ID != "abc" || reply.Status != 500 || reply.Error == nil {
		t.Fatalf("reply = %+v", reply)
	}

	body, err = encodeReply(env, &microservice.Envelope{Pattern: "echo", Status: 201})
	if err != nil {
		t.Fatalf("encodeReply: %v", err)
	}
	reply, _ = microservice.DecodeEnvelope(body)
	if reply.ID != "abc" {
		t.Fatalf("encodeReply must stamp the request id onto the reply, got %q", reply.ID)
	}
}

func TestServiceDescriptionShape(t *testing.T) {
	if serviceDesc.ServiceName != ServiceName {
		t.Errorf("ServiceName = %q, want %q", serviceDesc.ServiceName, ServiceName)
	}
	if len(serviceDesc.Methods) != 1 || serviceDesc.Methods[0].MethodName != "Dispatch" {
		t.Errorf("Methods = %+v, want one Dispatch method", serviceDesc.Methods)
	}
	stream := serviceDesc.Streams[0]
	if stream.StreamName != "Stream" || !stream.ClientStreams || !stream.ServerStreams {
		t.Errorf("Streams[0] = %+v, want a bidirectional Stream", stream)
	}
	if want := fmt.Sprintf("/%s/Dispatch", ServiceName); MethodDispatch != want {
		t.Errorf("MethodDispatch = %q, want %q", MethodDispatch, want)
	}
	if want := fmt.Sprintf("/%s/Stream", ServiceName); MethodStream != want {
		t.Errorf("MethodStream = %q, want %q", MethodStream, want)
	}
}
