package tcpmq

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

// testTimeout bounds every test so a hang fails instead of wedging CI.
const testTimeout = 3 * time.Second

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// echoDispatch answers with the payload it was given.
func echoDispatch(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{
		ID:      env.ID,
		Pattern: env.Pattern,
		Status:  200,
		Data:    env.Data,
	}, nil
}

// startServer binds a loopback listener on a free port and returns the transport
// and its address.
func startServer(t *testing.T, dispatch microservice.Dispatcher, mutate func(*Options)) (*Transport, string) {
	t.Helper()

	bound := make(chan net.Addr, 1)
	opts := Options{
		Addr:     "127.0.0.1:0",
		Logger:   testLogger(),
		OnListen: func(a net.Addr) { bound <- a },
	}
	if mutate != nil {
		mutate(&opts)
	}

	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Listen(ctx, []string{"echo", "slow", "event"}, dispatch) }()

	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(testTimeout):
			t.Error("Listen did not return after shutdown")
		}
	})

	select {
	case addr := <-bound:
		return srv, addr.String()
	case <-time.After(testTimeout):
		t.Fatal("server never bound")
		return nil, ""
	}
}

func newClient(t *testing.T, addr string, mutate func(*Options)) *Transport {
	t.Helper()

	opts := Options{DialAddr: addr, Logger: testLogger()}
	if mutate != nil {
		mutate(&opts)
	}
	client, err := New(opts)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func mustEnvelope(t *testing.T, pattern string, payload any) *microservice.Envelope {
	t.Helper()
	env, err := microservice.NewEnvelope(pattern, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return env
}

// ---------------------------------------------------------------- framing units

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"single byte", "x"},
		{"json object", `{"id":"1","pattern":"echo"}`},
		{"embedded newline", "{\n\"a\":1}"},
		{"embedded nul", "a\x00b"},
		{"multibyte", "héllo → 世界"},
		{"at the limit", strings.Repeat("a", 1024)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := bufio.NewWriter(&buf)
			if err := writeFrame(w, []byte(tc.payload), DefaultMaxFrameBytes); err != nil {
				t.Fatalf("writeFrame: %v", err)
			}

			r := bufio.NewReader(&buf)
			got, err := readFrame(r, DefaultMaxFrameBytes)
			if err != nil {
				t.Fatalf("readFrame: %v", err)
			}
			if string(got) != tc.payload {
				t.Fatalf("round trip changed the payload: got %q want %q", got, tc.payload)
			}
		})
	}
}

func TestFrameRoundTripBackToBack(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	want := []string{"one", "two", "three"}
	for _, p := range want {
		if err := writeFrame(w, []byte(p), DefaultMaxFrameBytes); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}

	r := bufio.NewReader(&buf)
	for _, expected := range want {
		got, err := readFrame(r, DefaultMaxFrameBytes)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if string(got) != expected {
			t.Fatalf("got %q want %q", got, expected)
		}
	}
	if _, err := readFrame(r, DefaultMaxFrameBytes); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after the last frame, got %v", err)
	}
}

// TestReadFrameSizeRejectsOversizedHeader is the memory-exhaustion guard: a header
// claiming 4 GiB must be rejected from the four header bytes alone, before any
// allocation and without touching the body.
func TestReadFrameSizeRejectsOversizedHeader(t *testing.T) {
	var buf bytes.Buffer
	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], 0xFFFFFFFF)
	buf.Write(header[:])
	buf.WriteString("sentinel")

	r := bufio.NewReader(&buf)
	size, err := readFrameSize(r, DefaultMaxFrameBytes)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if size != 0 {
		t.Fatalf("a rejected header must not report a size, got %d", size)
	}

	// The body was deliberately not consumed: the announced length is untrusted, so
	// the stream position is unknowable and the connection has to be dropped.
	rest, _ := io.ReadAll(r)
	if string(rest) != "sentinel" {
		t.Fatalf("reader consumed the body of a rejected frame: %q", rest)
	}
}

func TestReadFrameSizeRespectsCustomMax(t *testing.T) {
	var buf bytes.Buffer
	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], 65)
	buf.Write(header[:])

	if _, err := readFrameSize(bufio.NewReader(&buf), 64); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge for 65 > 64, got %v", err)
	}
}

func TestReadFrameRejectsZeroLength(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0})

	if _, err := readFrame(bufio.NewReader(&buf), DefaultMaxFrameBytes); !errors.Is(err, ErrZeroFrame) {
		t.Fatalf("expected ErrZeroFrame, got %v", err)
	}
}

func TestReadFrameTruncatedBody(t *testing.T) {
	var buf bytes.Buffer
	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], 16)
	buf.Write(header[:])
	buf.WriteString("only-8b")

	_, err := readFrame(bufio.NewReader(&buf), DefaultMaxFrameBytes)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("a truncated frame must not look like a clean EOF, got %v", err)
	}
}

func TestWriteFrameRejectsOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := writeFrame(w, bytes.Repeat([]byte("a"), 100), 64); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if err := writeFrame(w, nil, 64); !errors.Is(err, ErrZeroFrame) {
		t.Fatalf("expected ErrZeroFrame, got %v", err)
	}
}

// -------------------------------------------------------------- options & lifecycle

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{"no address", Options{}, "Addr is required"},
		{"negative frame limit", Options{Addr: ":0", MaxFrameBytes: -1}, "cannot be negative"},
		{"frame limit above the envelope limit", Options{Addr: ":0", MaxFrameBytes: DefaultMaxFrameBytes + 1}, "exceeds"},
		{"negative conns", Options{Addr: ":0", MaxConns: -1}, "cannot be negative"},
		{"dial address only", Options{DialAddr: "127.0.0.1:1"}, ""},
		{"bind address only", Options{Addr: ":0"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := New(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				_ = tr.Close()
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr, err := New(Options{Addr: ":0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if tr.opts.MaxFrameBytes != DefaultMaxFrameBytes {
		t.Errorf("MaxFrameBytes = %d", tr.opts.MaxFrameBytes)
	}
	if tr.opts.MaxConns != defaultMaxConns {
		t.Errorf("MaxConns = %d", tr.opts.MaxConns)
	}
	if tr.opts.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d", tr.opts.Concurrency)
	}
	if tr.opts.ReplyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("ReplyTimeout = %s", tr.opts.ReplyTimeout)
	}
}

func TestName(t *testing.T) {
	tr, err := New(Options{Addr: ":0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if got := tr.Name(); got != microservice.TransportTCP {
		t.Fatalf("Name() = %q want %q", got, microservice.TransportTCP)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	tr, err := New(Options{Addr: ":0"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := testContext(t)
	env := mustEnvelope(t, "echo", map[string]string{"a": "b"})

	if _, err := tr.Request(ctx, env, time.Second); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	if err := tr.Publish(ctx, env); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if err := tr.Listen(ctx, []string{"echo"}, echoDispatch); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Listen after Close = %v, want ErrClosed", err)
	}
}

// ------------------------------------------------------------------ end to end

func TestRequestReplyRoundTrip(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)
	client := newClient(t, addr, nil)

	ctx := testContext(t)
	env := mustEnvelope(t, "echo", map[string]string{"name": "Ada"})

	reply, err := client.Request(ctx, env, testTimeout)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID != env.ID {
		t.Errorf("reply id = %q want %q", reply.ID, env.ID)
	}
	if reply.Status != 200 {
		t.Errorf("reply status = %d", reply.Status)
	}
	if got := string(reply.Data); got != `{"name":"Ada"}` {
		t.Errorf("reply data = %s", got)
	}
	if n := client.pendingLen(); n != 0 {
		t.Errorf("pending map has %d entries after a successful request", n)
	}
}

func TestRequestReusesOneConnection(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)
	client := newClient(t, addr, nil)
	ctx := testContext(t)

	for i := 0; i < 5; i++ {
		if _, err := client.Request(ctx, mustEnvelope(t, "echo", i), testTimeout); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	client.dialMu.Lock()
	cc := client.cc
	client.dialMu.Unlock()
	if cc == nil || !cc.alive() {
		t.Fatal("the multiplexed connection was not retained across requests")
	}
}

func TestPublishIsFireAndForget(t *testing.T) {
	received := make(chan *microservice.Envelope, 1)
	dispatch := func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	_, addr := startServer(t, dispatch, nil)
	client := newClient(t, addr, nil)

	env := mustEnvelope(t, "event", map[string]int{"n": 1})
	if err := client.Publish(testContext(t), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-received:
		if got.ReplyTo != "" {
			t.Errorf("a published envelope must carry no ReplyTo, got %q", got.ReplyTo)
		}
	case <-time.After(testTimeout):
		t.Fatal("the server never received the published message")
	}

	// Nothing must come back on the connection: a reply nobody is reading would sit
	// in the client's reply reader and be mistaken for the answer to a later request.
	client.dialMu.Lock()
	cc := client.cc
	client.dialMu.Unlock()
	if cc == nil {
		t.Fatal("no client connection")
	}
	if n := client.pendingLen(); n != 0 {
		t.Errorf("pending map has %d entries after a publish", n)
	}
}

func TestConcurrentRequests(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)
	client := newClient(t, addr, nil)
	ctx := testContext(t)

	const goroutines = 64
	const perGoroutine = 8

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				env := mustEnvelopeConcurrent("echo", map[string]int{"g": g, "i": i})
				reply, err := client.Request(ctx, env, testTimeout)
				if err != nil {
					errs <- err
					return
				}
				if reply.ID != env.ID {
					errs <- errors.New("reply was routed to the wrong request")
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}

	if n := client.pendingLen(); n != 0 {
		t.Errorf("pending map has %d entries after all requests completed", n)
	}
}

// mustEnvelopeConcurrent is the t-free variant, safe to call from a goroutine.
func mustEnvelopeConcurrent(pattern string, payload any) *microservice.Envelope {
	env, err := microservice.NewEnvelope(pattern, payload)
	if err != nil {
		panic(err)
	}
	return env
}

func TestRequestTimeoutClearsPendingEntry(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	dispatch := func(ctx context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	_, addr := startServer(t, dispatch, nil)
	client := newClient(t, addr, nil)

	_, err := client.Request(context.Background(), mustEnvelope(t, "slow", nil), 150*time.Millisecond)
	if !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want ErrTimeout", err)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("a timed-out request leaked %d correlation entries", n)
	}
}

func TestCloseUnblocksPendingRequest(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	dispatch := func(ctx context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	_, addr := startServer(t, dispatch, nil)
	client := newClient(t, addr, nil)

	result := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), mustEnvelopeConcurrent("slow", nil), time.Minute)
		result <- err
	}()

	// Give the request time to reach the blocked handler before closing.
	time.Sleep(100 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-result:
		// Either sentinel is acceptable: Close races between signalling shutdown and
		// tearing the socket down, and both outcomes tell the caller the same thing.
		if !errors.Is(err, microservice.ErrClosed) && !errors.Is(err, ErrConnLost) {
			t.Fatalf("Request = %v, want ErrClosed or ErrConnLost", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock the pending Request")
	}
	if n := client.pendingLen(); n != 0 {
		t.Errorf("pending map has %d entries after Close", n)
	}
}

// ------------------------------------------------------------- hostile peers

func rawFrame(t *testing.T, env *microservice.Envelope) []byte {
	t.Helper()
	payload, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	frame := make([]byte, frameHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[frameHeaderBytes:], payload)
	return frame
}

func dialRaw(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, testTimeout)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestOversizedLengthPrefixDropsConnectionOnly(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)

	conn := dialRaw(t, addr)
	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], 0xFFFFFFFF)
	if _, err := conn.Write(header[:]); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The server must hang up rather than try to allocate 4 GiB.
	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the server kept the connection open after an oversized length prefix")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the server neither replied nor closed the connection")
	}

	// And it must still be serving everyone else.
	client := newClient(t, addr, nil)
	if _, err := client.Request(testContext(t), mustEnvelope(t, "echo", 1), testTimeout); err != nil {
		t.Fatalf("the server stopped serving after a hostile frame: %v", err)
	}
}

func TestTruncatedFrameDropsConnectionOnly(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)

	conn := dialRaw(t, addr)
	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], 4096)
	if _, err := conn.Write(append(header[:], []byte(`{"pattern":"echo"`)...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	client := newClient(t, addr, nil)
	if _, err := client.Request(testContext(t), mustEnvelope(t, "echo", 1), testTimeout); err != nil {
		t.Fatalf("the server stopped serving after a truncated frame: %v", err)
	}
}

func TestMalformedJSONFrameKeepsTheConnection(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)
	conn := dialRaw(t, addr)

	// A frame whose length is honest but whose payload is not JSON. The stream is
	// still aligned, so the server must skip it and read the next frame.
	garbage := []byte("this is not json at all")
	frame := make([]byte, frameHeaderBytes+len(garbage))
	binary.BigEndian.PutUint32(frame, uint32(len(garbage)))
	copy(frame[frameHeaderBytes:], garbage)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	env := mustEnvelope(t, "echo", map[string]string{"ok": "yes"})
	env.ReplyTo = ReplyToConn
	if _, err := conn.Write(rawFrame(t, env)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	reply, err := readFrame(bufio.NewReader(conn), DefaultMaxFrameBytes)
	if err != nil {
		t.Fatalf("a malformed payload killed the connection: %v", err)
	}
	got, err := microservice.DecodeEnvelope(reply)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if got.ID != env.ID {
		t.Fatalf("reply id = %q want %q", got.ID, env.ID)
	}
}

// TestEmptyPatternFrameIsRejected covers the envelope decoder's own guard reaching
// the transport: a syntactically valid JSON object that is not a usable envelope.
func TestMissingPatternFrameKeepsTheConnection(t *testing.T) {
	_, addr := startServer(t, echoDispatch, nil)
	conn := dialRaw(t, addr)

	payload := []byte(`{"id":"abc"}`)
	frame := make([]byte, frameHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[frameHeaderBytes:], payload)
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	env := mustEnvelope(t, "echo", 42)
	env.ReplyTo = ReplyToConn
	if _, err := conn.Write(rawFrame(t, env)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := readFrame(bufio.NewReader(conn), DefaultMaxFrameBytes); err != nil {
		t.Fatalf("a patternless envelope killed the connection: %v", err)
	}
}

// ------------------------------------------------------------------ resilience

func TestClientRedialsAfterServerDropsTheConnection(t *testing.T) {
	_, addr := startServer(t, echoDispatch, func(o *Options) {
		// A short idle timeout makes the server reclaim the client's connection,
		// which is the exact condition the redial path exists for.
		o.IdleTimeout = 150 * time.Millisecond
	})
	client := newClient(t, addr, nil)
	ctx := testContext(t)

	if _, err := client.Request(ctx, mustEnvelope(t, "echo", 1), testTimeout); err != nil {
		t.Fatalf("first request: %v", err)
	}

	client.dialMu.Lock()
	first := client.cc
	client.dialMu.Unlock()

	// Wait for the server to time the connection out.
	deadline := time.Now().Add(testTimeout)
	for first.alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if first.alive() {
		t.Fatal("the server never reclaimed the idle connection")
	}

	if _, err := client.Request(ctx, mustEnvelope(t, "echo", 2), testTimeout); err != nil {
		t.Fatalf("request after the connection dropped: %v", err)
	}

	client.dialMu.Lock()
	second := client.cc
	client.dialMu.Unlock()
	if second == first {
		t.Fatal("the client reused a dead connection instead of redialing")
	}
}

func TestDialFailureIsReportedNotRetriedForever(t *testing.T) {
	// Port 1 on loopback is reliably closed; MaxDialAttempts bounds the retries.
	client := newClient(t, "127.0.0.1:1", func(o *Options) {
		o.MaxDialAttempts = 2
		o.DialTimeout = 200 * time.Millisecond
	})

	start := time.Now()
	err := client.Publish(context.Background(), mustEnvelope(t, "echo", 1))
	if err == nil {
		t.Fatal("expected a dial error")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Fatalf("error %q does not mention the dial failure", err)
	}
	if elapsed := time.Since(start); elapsed > testTimeout {
		t.Fatalf("dial retries took %s, which is not bounded by MaxDialAttempts", elapsed)
	}
}

// TestMaxConnsBoundsAcceptedConnections asserts the accept loop honours its limit.
// With one slot, a second connection sits in the kernel backlog and is served only
// once the first is released — which is the behaviour that keeps a connection flood
// from exhausting file descriptors.
func TestMaxConnsBoundsAcceptedConnections(t *testing.T) {
	_, addr := startServer(t, echoDispatch, func(o *Options) {
		o.MaxConns = 1
		o.IdleTimeout = testTimeout
	})

	first := dialRaw(t, addr)
	env1 := mustEnvelope(t, "echo", 1)
	env1.ReplyTo = ReplyToConn
	if _, err := first.Write(rawFrame(t, env1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = first.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := readFrame(bufio.NewReader(first), DefaultMaxFrameBytes); err != nil {
		t.Fatalf("the first connection was not served: %v", err)
	}

	second := dialRaw(t, addr)
	env2 := mustEnvelope(t, "echo", 2)
	env2.ReplyTo = ReplyToConn
	if _, err := second.Write(rawFrame(t, env2)); err != nil {
		t.Fatalf("write: %v", err)
	}

	secondReader := bufio.NewReader(second)
	_ = second.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := readFrame(secondReader, DefaultMaxFrameBytes); err == nil {
		t.Fatal("the second connection was served while MaxConns was exhausted")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("expected a read timeout on the queued connection, got %v", err)
	}

	// Releasing the slot must let the queued connection through.
	_ = first.Close()
	_ = second.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := readFrame(secondReader, DefaultMaxFrameBytes); err != nil {
		t.Fatalf("the queued connection was never served after the slot freed: %v", err)
	}
}

// TestGracefulShutdownStillDeliversAnInFlightReply covers why the shutdown watchdog
// expires the read deadline instead of closing the socket: a request already being
// handled must still be answered, or every deploy turns in-flight calls into
// client-side timeouts.
func TestGracefulShutdownStillDeliversAnInFlightReply(t *testing.T) {
	handling := make(chan struct{})
	release := make(chan struct{})

	dispatch := func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		select {
		case handling <- struct{}{}:
		default:
		}
		<-release
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	bound := make(chan net.Addr, 1)
	srv, err := New(Options{
		Addr:     "127.0.0.1:0",
		Logger:   testLogger(),
		OnListen: func(a net.Addr) { bound <- a },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	listenDone := make(chan error, 1)
	go func() { listenDone <- srv.Listen(ctx, []string{"slow"}, dispatch) }()

	var addr string
	select {
	case a := <-bound:
		addr = a.String()
	case <-time.After(testTimeout):
		t.Fatal("never bound")
	}

	client := newClient(t, addr, nil)
	result := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), mustEnvelopeConcurrent("slow", nil), testTimeout)
		result <- err
	}()

	select {
	case <-handling:
	case <-time.After(testTimeout):
		t.Fatal("the handler never started")
	}

	// Shut the listener down while the handler is mid-flight, then let it finish.
	cancel()
	time.Sleep(100 * time.Millisecond)
	close(release)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("an in-flight request was dropped by shutdown: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("the in-flight reply never arrived")
	}

	select {
	case err := <-listenDone:
		if err != nil {
			t.Fatalf("a cancelled Listen must return nil, got %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Listen did not return")
	}
}

func TestListenRejectsNilDispatcher(t *testing.T) {
	tr, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if err := tr.Listen(testContext(t), nil, nil); err == nil {
		t.Fatal("expected an error for a nil dispatcher")
	}
}

func TestListenReturnsOnContextCancel(t *testing.T) {
	bound := make(chan net.Addr, 1)
	tr, err := New(Options{Addr: "127.0.0.1:0", Logger: testLogger(), OnListen: func(a net.Addr) { bound <- a }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Listen(ctx, []string{"echo"}, echoDispatch) }()

	select {
	case <-bound:
	case <-time.After(testTimeout):
		t.Fatal("never bound")
	}
	if tr.Addr() == "" {
		t.Error("Addr() is empty while listening")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled Listen must return nil, got %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Listen did not return after cancellation")
	}
	if tr.Addr() != "" {
		t.Error("Addr() is still set after Listen returned")
	}
}
