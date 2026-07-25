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

// ErrConnLost means the connection died before the reply arrived. It is
// deliberately distinct from microservice.ErrTimeout: a lost connection says
// nothing about whether the server processed the message, so a caller must decide
// for itself whether retrying is safe.
var ErrConnLost = errors.New("tcpmq: connection lost before the reply arrived")

// clientConn is one multiplexed connection to a server.
//
// A fresh connection per request would be simpler and correct, but it pays a TCP
// handshake — and a TLS handshake — for every message, and it burns a local port
// per in-flight call, so a burst of requests can exhaust the ephemeral port range
// and then sit in TIME_WAIT for a minute. Instead one connection is shared: every
// request writes onto it under a mutex, a single background reader demultiplexes
// inbound frames by Envelope.ID, and the connection is redialled on demand when it
// dies.
type clientConn struct {
	conn net.Conn
	bw   *bufio.Writer

	// writeMu serialises frame writes exactly as on the server side: concurrent
	// requests share this socket, and an interleaved header/body pair would
	// desynchronise the stream for every message after it.
	writeMu sync.Mutex

	// done is closed when the connection is unusable, so a Request waiting for a
	// reply fails immediately instead of waiting out its whole timeout.
	done      chan struct{}
	closeOnce sync.Once
}

func newClientConn(conn net.Conn) *clientConn {
	return &clientConn{
		conn: conn,
		bw:   bufio.NewWriterSize(conn, connBufferBytes),
		done: make(chan struct{}),
	}
}

func (c *clientConn) send(payload []byte, max int, timeout time.Duration) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if timeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	if err := writeFrame(c.bw, payload, max); err != nil {
		// A failed write leaves an unknown number of bytes on the wire, so the
		// stream can no longer be trusted; retire the connection.
		c.close()
		return err
	}
	return nil
}

func (c *clientConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *clientConn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Publish sends a fire-and-forget envelope. ReplyTo is cleared so the server does
// not write a reply nobody is waiting for.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("tcpmq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	env.ReplyTo = ""
	payload, err := env.Encode()
	if err != nil {
		return fmt.Errorf("tcpmq: cannot encode envelope: %w", err)
	}

	cc, err := t.dial(ctx)
	if err != nil {
		return err
	}
	if err := cc.send(payload, t.opts.MaxFrameBytes, t.opts.WriteTimeout); err != nil {
		return fmt.Errorf("tcpmq: publish %q: %w", env.Pattern, err)
	}
	return nil
}

// Request sends an envelope and waits for the correlated reply.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("tcpmq: cannot send a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}

	if timeout <= 0 {
		timeout = t.opts.ReplyTimeout
	}
	// Both deadlines are honoured: whichever of the caller's context and the
	// per-call timeout fires first ends the wait.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if env.ID == "" {
		env.ID = microservice.NewID()
	}
	env.ReplyTo = ReplyToConn

	payload, err := env.Encode()
	if err != nil {
		return nil, fmt.Errorf("tcpmq: cannot encode envelope: %w", err)
	}

	// Buffered so a reply that arrives after we have given up never blocks the
	// single reader goroutine — one blocked reader would stall replies for every
	// other in-flight request on this connection.
	replyCh := make(chan *microservice.Envelope, 1)

	// Register before writing. The server can answer faster than this goroutine is
	// rescheduled, and a reply that arrives before its correlation entry exists is
	// unroutable and lost forever.
	t.pendingMu.Lock()
	t.pending[env.ID] = replyCh
	t.pendingMu.Unlock()

	// Unconditional cleanup, on every path including timeout and connection loss.
	// Deleting only on success leaks one map entry per unanswered request, which a
	// peer that simply stops replying can grow without bound.
	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, env.ID)
		t.pendingMu.Unlock()
	}()

	cc, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := cc.send(payload, t.opts.MaxFrameBytes, t.opts.WriteTimeout); err != nil {
		return nil, fmt.Errorf("tcpmq: request %q: %w", env.Pattern, err)
	}

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-cc.done:
		return nil, fmt.Errorf("%w (pattern %q)", ErrConnLost, env.Pattern)
	case <-ctx.Done():
		return nil, mapTimeout(ctx.Err())
	case <-t.closed:
		return nil, microservice.ErrClosed
	}
}

// dial returns the shared connection, establishing it if necessary.
//
// The retry loop exists because the peer of a broker-less transport is an ordinary
// process that restarts: without it, every deploy of the server would surface as a
// hard error in the client instead of a brief pause.
func (t *Transport) dial(ctx context.Context) (*clientConn, error) {
	t.dialMu.Lock()
	defer t.dialMu.Unlock()

	if t.cc != nil {
		if t.cc.alive() {
			return t.cc, nil
		}
		t.cc = nil
	}

	addr := t.opts.DialAddr
	if addr == "" {
		addr = t.opts.Addr
	}
	if addr == "" {
		return nil, errors.New("tcpmq: no dial address configured")
	}

	backoff := 50 * time.Millisecond
	const maxBackoff = time.Second
	var lastErr error

	for attempt := 1; attempt <= t.opts.MaxDialAttempts; attempt++ {
		if t.isClosed() {
			return nil, microservice.ErrClosed
		}
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("tcpmq: dial %s: %w", addr, lastErr)
			}
			return nil, mapTimeout(err)
		}

		conn, err := t.dialOnce(ctx, addr)
		if err == nil {
			cc := newClientConn(conn)
			t.cc = cc
			t.cliWG.Add(1)
			go t.readReplies(cc)
			return cc, nil
		}
		lastErr = err

		if attempt == t.opts.MaxDialAttempts {
			break
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("tcpmq: dial %s: %w", addr, lastErr)
		case <-t.closed:
			return nil, microservice.ErrClosed
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return nil, fmt.Errorf("tcpmq: dial %s after %d attempts: %w", addr, t.opts.MaxDialAttempts, lastErr)
}

// dialOnce performs a single dial, upgrading to TLS when configured. The TLS
// handshake is done with HandshakeContext rather than implicitly on first read, so
// a server that accepts the TCP connection and then stalls cannot hold the caller
// past its deadline.
func (t *Transport) dialOnce(ctx context.Context, addr string) (net.Conn, error) {
	dialCtx := ctx
	if t.opts.DialTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, t.opts.DialTimeout)
		defer cancel()
	}

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if t.opts.ClientTLSConfig == nil {
		return conn, nil
	}

	tlsConn := tls.Client(conn, t.opts.ClientTLSConfig)
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tlsConn, nil
}

// readReplies demultiplexes inbound frames onto the goroutines waiting for them.
// There is exactly one of these per connection, which is what makes a shared
// connection safe to read: two readers on one socket would each get a fragment of
// every frame.
//
// No read deadline is set: this connection is deliberately long-lived and idle
// between requests. The server's IdleTimeout is what reclaims it, and the resulting
// EOF here retires the connection so the next Publish or Request redials.
func (t *Transport) readReplies(cc *clientConn) {
	defer t.cliWG.Done()
	defer cc.close()

	reader := bufio.NewReaderSize(cc.conn, connBufferBytes)

	for {
		payload, err := readFrame(reader, t.opts.MaxFrameBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				t.log.Debug("tcpmq reply reader stopped", slog.Any("error", err))
			}
			return
		}

		env, err := microservice.DecodeEnvelope(payload)
		if err != nil {
			// Frame-aligned, so recoverable: skip this reply and keep reading. The
			// request it belonged to falls back on its timeout.
			t.log.Warn("tcpmq dropping undecodable reply", slog.Any("error", err))
			continue
		}
		t.deliver(env)
	}
}

// deliver hands a reply to its waiting Request, if one is still there.
func (t *Transport) deliver(env *microservice.Envelope) {
	t.pendingMu.Lock()
	ch, waiting := t.pending[env.ID]
	t.pendingMu.Unlock()

	if !waiting {
		// A reply for a request that already timed out or was abandoned. Dropping
		// it is correct; the alternative — keeping the entry alive "just in case" —
		// is the leak this map is careful to avoid.
		return
	}

	// Non-blocking on a buffer of one: a duplicate reply must not park the reader.
	select {
	case ch <- env:
	default:
	}
}
