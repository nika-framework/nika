package tcpmq

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

// connBufferBytes sizes the per-connection buffered reader and writer. Frames are
// usually a few hundred bytes, so a small buffer keeps 1024 connections cheap
// while still turning the header write and the body write into one syscall.
const connBufferBytes = 8 << 10

// shutdownPokeInterval is how often the per-connection watchdog re-expires the read
// deadline while waiting for its read loop to notice shutdown.
const shutdownPokeInterval = 20 * time.Millisecond

// Listen binds the configured address and serves messages until ctx is cancelled
// or the transport is closed.
//
// patterns are ignored. TCP has no broker to filter at, so every frame that
// arrives is dispatched and the core Router decides which handler — if any — owns
// the subject. That is also why a pattern that matches nothing still gets a 404
// reply rather than being silently dropped: the sender is on the other end of this
// very connection and can be told.
func (t *Transport) Listen(ctx context.Context, _ []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("tcpmq: a dispatcher is required")
	}
	if t.opts.Addr == "" {
		return errors.New("tcpmq: Options.Addr is required to listen")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	ln, err := net.Listen("tcp", t.opts.Addr)
	if err != nil {
		return fmt.Errorf("tcpmq: listen on %q: %w", t.opts.Addr, err)
	}
	if t.opts.TLSConfig != nil {
		ln = tls.NewListener(ln, t.opts.TLSConfig)
	}

	t.srvMu.Lock()
	if t.listener != nil {
		t.srvMu.Unlock()
		_ = ln.Close()
		return errors.New("tcpmq: already listening")
	}
	t.listener = ln
	t.srvMu.Unlock()

	if t.opts.OnListen != nil {
		t.opts.OnListen(ln.Addr())
	}
	t.log.Info("tcpmq listening", slog.String("addr", ln.Addr().String()))

	// stopServing is closed when this Listen returns for any reason. Connection
	// goroutines watch it as well as ctx, because the supervisor may call Listen
	// again after a failure and connections belonging to the previous attempt must
	// not still be dispatching into the dispatcher it owned.
	stopServing := make(chan struct{})

	// Accept has no context, so cancellation is implemented by closing the
	// listener from a watchdog; the blocked Accept then returns net.ErrClosed.
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		select {
		case <-ctx.Done():
		case <-t.closed:
		case <-stopServing:
		}
		_ = ln.Close()
	}()

	defer func() {
		close(stopServing)
		<-watchdogDone

		t.srvMu.Lock()
		if t.listener == ln {
			t.listener = nil
		}
		t.srvMu.Unlock()

		// Connection goroutines stop reading as soon as they see stopServing, and
		// finish their in-flight handlers first, so this normally drains at once.
		if !waitTimeout(&t.srvWG, t.opts.ShutdownTimeout) {
			// A peer that refuses to release the connection must not be able to
			// hold Listen open, because Listen holding means the supervisor never
			// rebinds. Force the sockets closed and stop waiting politely.
			t.closeTrackedConns()
			_ = waitTimeout(&t.srvWG, t.opts.ShutdownTimeout)
		}
	}()

	slots := make(chan struct{}, t.opts.MaxConns)

	for {
		// The connection slot is taken *before* Accept, not after. Accepting first
		// and then closing over the limit still costs an fd per attacker
		// connection and turns the accept loop into a busy loop; leaving the
		// connection in the kernel's backlog costs us nothing and applies real
		// backpressure at the TCP layer.
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		case <-t.closed:
			return microservice.ErrClosed
		}

		conn, err := ln.Accept()
		if err != nil {
			<-slots

			if ctx.Err() != nil {
				return nil
			}
			if t.isClosed() {
				return microservice.ErrClosed
			}
			// A closed listener means shutdown, not failure — returning an error
			// would make the supervisor rebind and log a spurious failure.
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("tcpmq: accept: %w", err)
		}

		t.trackConn(conn)
		t.srvWG.Add(1)
		go func() {
			defer t.srvWG.Done()
			defer func() { <-slots }()
			defer t.untrackConn(conn)
			t.serveConn(ctx, stopServing, conn, dispatch)
		}()
	}
}

// closeTrackedConns force-closes every live connection. It is the escalation path
// for a shutdown that a peer will not cooperate with.
func (t *Transport) closeTrackedConns() {
	t.srvMu.Lock()
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.srvMu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
}

func (t *Transport) trackConn(conn net.Conn) {
	t.srvMu.Lock()
	t.conns[conn] = struct{}{}
	t.srvMu.Unlock()
}

func (t *Transport) untrackConn(conn net.Conn) {
	t.srvMu.Lock()
	delete(t.conns, conn)
	t.srvMu.Unlock()
}

// serveConn reads frames from one connection until it dies, dispatching each and
// writing replies back on the same connection.
func (t *Transport) serveConn(ctx context.Context, stopServing <-chan struct{}, conn net.Conn, dispatch microservice.Dispatcher) {
	peer := conn.RemoteAddr().String()

	// A goroutine parked in Read cannot be cancelled, so a watchdog unblocks it by
	// expiring the read deadline rather than by closing the socket. That difference
	// is the whole graceful-shutdown story: the socket stays writable, so handlers
	// that are already running still deliver their replies, and only then does the
	// deferred Close run. Closing here instead would turn every in-flight request
	// into a client-side timeout on every deploy.
	done := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		select {
		case <-ctx.Done():
		case <-t.closed:
		case <-stopServing:
		case <-done:
			return
		}
		// Poke repeatedly rather than once. The read loop resets the deadline before
		// every header read, so a single poke can be overwritten by a reset that
		// races with it, leaving the loop parked for a whole IdleTimeout with nothing
		// left to wake it.
		for {
			_ = conn.SetReadDeadline(time.Now())
			select {
			case <-done:
				return
			case <-time.After(shutdownPokeInterval):
			}
		}
	}()

	// Ordering matters: the socket must stay open until the handlers that write
	// replies onto it have finished, so conn.Close is deferred first and therefore
	// runs last (deferred functions run in reverse order).
	defer func() {
		_ = conn.Close()
		close(done)
		<-watchdogDone
	}()

	var handlers sync.WaitGroup
	defer handlers.Wait()

	reader := bufio.NewReaderSize(conn, connBufferBytes)
	writer := bufio.NewWriterSize(conn, connBufferBytes)

	// One mutex per connection guards every frame write. Handlers run
	// concurrently and all reply on this single socket; without serialisation two
	// replies would interleave their header and body bytes, and the peer would
	// read a length prefix followed by another message's payload. That is not a
	// lost message — it is a permanently desynchronised stream that corrupts every
	// frame after it.
	var writeMu sync.Mutex

	for {
		// Check for shutdown before arming a fresh idle deadline, so the common case
		// exits at once instead of waiting for the watchdog to poke it.
		select {
		case <-ctx.Done():
			return
		case <-t.closed:
			return
		case <-stopServing:
			return
		default:
		}

		// A generous idle deadline while waiting for the next header reclaims
		// connections from peers that completed the handshake and went silent.
		if t.opts.IdleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(t.opts.IdleTimeout))
		}

		size, err := readFrameSize(reader, t.opts.MaxFrameBytes)
		if err != nil {
			t.logReadEnd(ctx, peer, err, "header")
			return
		}

		// A tighter deadline for the body: the peer has announced its length, so
		// it is expected to deliver it promptly. This is the slow-loris guard.
		if t.opts.ReadTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(t.opts.ReadTimeout))
		}

		payload, err := readFrameBody(reader, size)
		if err != nil {
			t.logReadEnd(ctx, peer, err, "body")
			return
		}

		env, decodeErr := microservice.DecodeEnvelope(payload)
		if decodeErr != nil {
			// Recoverable. We consumed exactly the announced number of bytes, so
			// the stream is still frame-aligned and the next message will parse.
			// Dropping the connection here would let one buggy publisher take down
			// a link shared by healthy ones.
			t.log.Warn("tcpmq dropping undecodable frame",
				slog.String("peer", peer),
				slog.Int("bytes", size),
				slog.Any("error", decodeErr),
			)
			continue
		}

		if !t.acquire(ctx) {
			return
		}

		handlers.Add(1)
		t.srvWG.Add(1)
		go func(env *microservice.Envelope) {
			defer t.srvWG.Done()
			defer handlers.Done()
			defer t.release()
			t.handleMessage(ctx, conn, writer, &writeMu, env, dispatch)
		}(env)
	}
}

// logReadEnd classifies why a read loop ended. A client hanging up normally (EOF),
// our own shutdown (net.ErrClosed, or the expired read deadline the watchdog sets)
// and a cancelled context are all routine; logging them as failures would make a
// healthy service look broken on every deploy.
func (t *Transport) logReadEnd(ctx context.Context, peer string, err error, stage string) {
	switch {
	case errors.Is(err, io.EOF):
		return
	case errors.Is(err, net.ErrClosed):
		return
	case ctx.Err() != nil:
		return
	case t.isClosed():
		return
	}

	t.log.Warn("tcpmq connection closed",
		slog.String("peer", peer),
		slog.String("stage", stage),
		slog.Any("error", err),
	)
}

// handleMessage dispatches one envelope and, when a reply was asked for, frames it
// back onto the same connection.
func (t *Transport) handleMessage(
	ctx context.Context,
	conn net.Conn,
	writer *bufio.Writer,
	writeMu *sync.Mutex,
	env *microservice.Envelope,
	dispatch microservice.Dispatcher,
) {
	reply, err := dispatch(ctx, env)

	// An empty ReplyTo is a fire-and-forget event: dispatch it for its side
	// effects and throw the reply away rather than writing an unwanted frame the
	// peer is not reading.
	if env.ReplyTo == "" {
		if err != nil {
			t.log.Warn("tcpmq dispatch failed",
				slog.String("pattern", env.Pattern),
				slog.Any("error", err),
			)
		}
		return
	}

	if reply == nil {
		detail := "handler produced no reply"
		if err != nil {
			detail = err.Error()
		}
		reply = errorReply(env, 500, "DISPATCH_ERROR", detail)
	}
	// The correlation id always comes from the request; a handler must not be able
	// to redirect a reply to another caller's pending entry.
	reply.ID = env.ID

	data, encodeErr := reply.Encode()
	if encodeErr != nil {
		t.log.Error("tcpmq cannot encode reply",
			slog.String("pattern", env.Pattern),
			slog.Any("error", encodeErr),
		)
		data, encodeErr = errorReply(env, 500, "ENCODE_ERROR", "reply could not be encoded").Encode()
		if encodeErr != nil {
			return
		}
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	if t.opts.WriteTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(t.opts.WriteTimeout))
	}
	if err := writeFrame(writer, data, t.opts.MaxFrameBytes); err != nil {
		// The peer is gone or stalled. The read loop will notice on its next read;
		// there is nothing useful to do from here.
		t.log.Warn("tcpmq cannot write reply",
			slog.String("pattern", env.Pattern),
			slog.Any("error", err),
		)
	}
}
