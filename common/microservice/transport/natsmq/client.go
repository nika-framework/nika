package natsmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

// Publish sends a fire-and-forget envelope.
//
// NATS publishes are asynchronous and buffered, so a nil return means the message
// was queued to the connection, not that a subscriber received it — core NATS is
// at-most-once and has no acknowledgement. Call Ping (which flushes) when a
// publish must be known to have reached the server.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("natsmq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	subject, err := subjectFor(t.prefix, env.Pattern)
	if err != nil {
		return err
	}

	nc, err := t.conn()
	if err != nil {
		return err
	}

	env.ReplyTo = ""
	payload, err := env.Encode()
	if err != nil {
		return fmt.Errorf("natsmq: cannot encode envelope: %w", err)
	}

	if err := nc.Publish(subject, payload); err != nil {
		return fmt.Errorf("natsmq: publish %q: %w", subject, mapNATSError(err))
	}
	return nil
}

// Request sends an envelope and waits for the reply.
//
// No correlation map, no reply inbox bookkeeping and no pending-entry cleanup
// appear here, because NATS does request/reply natively: the client library
// allocates a per-connection wildcard inbox, tags the request with a unique reply
// subject, and matches the response for us. Hand-rolling correlation on top of that
// would add a second identifier to keep consistent with Envelope.ID and a second
// map to leak, for no benefit.
//
// Envelope.ID is still set and still checked below — it is what makes the exchange
// verifiable end to end and identical in shape to the transports that have to
// correlate themselves.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("natsmq: cannot send a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}

	subject, err := subjectFor(t.prefix, env.Pattern)
	if err != nil {
		return nil, err
	}

	nc, err := t.conn()
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = t.replyTimeout
	}
	// Both deadlines are honoured: whichever of the caller's context and the
	// per-call timeout fires first ends the wait.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if env.ID == "" {
		env.ID = microservice.NewID()
	}
	// The reply address is supplied by the protocol, not the envelope; leaving a
	// stale ReplyTo in place would make the responder publish a second copy of the
	// reply to whatever it named.
	env.ReplyTo = ""

	payload, err := env.Encode()
	if err != nil {
		return nil, fmt.Errorf("natsmq: cannot encode envelope: %w", err)
	}

	// Close must unblock a pending Request, and RequestWithContext only watches the
	// context, so the transport's shutdown signal is folded into a derived context.
	reqCtx, stop := t.contextWithClose(ctx)
	defer stop()

	msg, err := nc.RequestWithContext(reqCtx, subject, payload)
	if err != nil {
		if t.isClosed() {
			return nil, microservice.ErrClosed
		}
		return nil, fmt.Errorf("natsmq: request %q: %w", subject, mapNATSError(err))
	}

	reply, err := microservice.DecodeEnvelope(msg.Data)
	if err != nil {
		return nil, fmt.Errorf("natsmq: malformed reply for %q: %w", subject, err)
	}
	if reply.ID != env.ID {
		// The inbox is per-request, so this should be impossible; treating it as an
		// error rather than returning it keeps a mismatched reply from being handed
		// to a caller as if it answered their question.
		return nil, fmt.Errorf("natsmq: reply id %q does not match request id %q", reply.ID, env.ID)
	}
	return reply, nil
}

// contextWithClose derives a context that is also cancelled when the transport is
// closed, so a blocked Request returns instead of waiting out its timeout.
func (t *Transport) contextWithClose(ctx context.Context) (context.Context, func()) {
	derived, cancel := context.WithCancel(ctx)

	done := make(chan struct{})
	go func() {
		select {
		case <-t.closed:
			cancel()
		case <-derived.Done():
		case <-done:
		}
	}()

	return derived, func() {
		close(done)
		cancel()
	}
}
