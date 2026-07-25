package redismq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nika-framework/nika/common/microservice"
)

// Publish sends a fire-and-forget envelope.
//
// PUBLISH reports how many subscribers received the message, and zero is not an
// error at the protocol level. It is not treated as one here either, because a
// broadcast event legitimately has no listeners — but it is worth restating that
// this is where at-most-once delivery bites: nothing about a successful Publish
// implies anyone got it.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("redismq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	channel, err := messageChannel(t.prefix, env.Pattern)
	if err != nil {
		return err
	}

	env.ReplyTo = ""
	payload, err := env.Encode()
	if err != nil {
		return fmt.Errorf("redismq: cannot encode envelope: %w", err)
	}

	if err := t.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("redismq: publish %q: %w", channel, mapTimeout(err))
	}
	return nil
}

// Request publishes an envelope and waits for the correlated reply on this
// client's private inbox.
//
// The design is one long-lived reply subscription per client, correlated by
// Envelope.ID, rather than a fresh subscription per request. A per-request
// subscription costs a SUBSCRIBE and an UNSUBSCRIBE round trip on every call and
// leaves a window in which the reply can arrive before the subscription exists —
// and pub/sub does not buffer, so such a reply is lost permanently rather than
// delivered late.
//
// The subscribe-before-publish ordering still matters for the shared inbox; it is
// just satisfied once instead of per call. ensureReplies below blocks on the
// server's subscription confirmation before this function publishes anything, so
// by the time the request is on the wire the inbox provably exists.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("redismq: cannot send a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}

	channel, err := messageChannel(t.prefix, env.Pattern)
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

	if err := t.ensureReplies(ctx); err != nil {
		return nil, err
	}

	if env.ID == "" {
		env.ID = microservice.NewID()
	}
	env.ReplyTo = t.replyChannel

	payload, err := env.Encode()
	if err != nil {
		return nil, fmt.Errorf("redismq: cannot encode envelope: %w", err)
	}

	return t.awaitReply(ctx, env.ID, func() error {
		if err := t.client.Publish(ctx, channel, payload).Err(); err != nil {
			return fmt.Errorf("redismq: publish %q: %w", channel, mapTimeout(err))
		}
		return nil
	})
}

// awaitReply registers a correlation entry, runs send, and waits for the reply.
//
// The three steps are one function on purpose. Registering has to happen before
// send — the peer can answer faster than this goroutine is rescheduled, and a reply
// with no correlation entry is unroutable and, pub/sub having no buffer, gone for
// good. And the entry has to be removed on *every* exit path, which only a defer in
// the same frame as the registration can guarantee.
func (t *Transport) awaitReply(ctx context.Context, id string, send func() error) (*microservice.Envelope, error) {
	// Buffered so a reply arriving after we have given up never blocks the single
	// reply reader — one blocked reader would stall replies for every other
	// in-flight request.
	replyCh := make(chan *microservice.Envelope, 1)

	t.pendingMu.Lock()
	t.pending[id] = replyCh
	t.pendingMu.Unlock()

	// Unconditional cleanup, timeout and send failure included. Deleting only on
	// success leaks one entry per unanswered request, which a peer that simply stops
	// replying can grow without bound.
	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
	}()

	if err := send(); err != nil {
		return nil, err
	}

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ctx.Done():
		return nil, mapTimeout(ctx.Err())
	case <-t.closed:
		return nil, microservice.ErrClosed
	}
}

// ensureReplies establishes the client's reply inbox, once.
//
// It is lazy rather than done in New so a process that only serves handlers never
// pays for a subscription it will not use, and it is guarded by a mutex rather than
// a sync.Once so a failure (Redis briefly down) can be retried by the next Request
// instead of poisoning the transport for its lifetime.
func (t *Transport) ensureReplies(ctx context.Context) error {
	t.replyMu.Lock()
	defer t.replyMu.Unlock()

	if t.replySub != nil {
		return nil
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	// Empty Subscribe first so the subscription error is not discarded; see the
	// same note in Listen.
	sub := t.client.Subscribe(ctx)
	if err := sub.Subscribe(ctx, t.replyChannel); err != nil {
		_ = sub.Close()
		return fmt.Errorf("redismq: subscribing reply inbox %q: %w", t.replyChannel, err)
	}
	// Block for the server's confirmation. This is the barrier that makes
	// subscribe-before-publish true: it must succeed before any Request publishes,
	// or the reply could be published into a channel nobody is subscribed to yet.
	// It must also happen before Channel, which disables the Receive family.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return fmt.Errorf("redismq: confirming reply inbox %q: %w", t.replyChannel, mapTimeout(err))
	}

	messages := sub.Channel()
	t.replySub = sub
	t.replyWG.Add(1)
	go t.readReplies(messages)

	return nil
}

// readReplies demultiplexes the shared inbox onto the goroutines waiting for each
// reply. There is exactly one of these per transport; it exits when Close shuts the
// subscription down, which closes the channel.
func (t *Transport) readReplies(messages <-chan *redis.Message) {
	defer t.replyWG.Done()

	for {
		select {
		case <-t.closed:
			return
		case msg, open := <-messages:
			if !open {
				return
			}
			env, err := microservice.DecodeEnvelope([]byte(msg.Payload))
			if err != nil {
				t.log.Warn("redismq dropping undecodable reply",
					slog.String("channel", msg.Channel),
					slog.Any("error", err),
				)
				continue
			}
			t.deliver(env)
		}
	}
}

// deliver hands a reply to its waiting Request, if one is still there.
func (t *Transport) deliver(env *microservice.Envelope) {
	t.pendingMu.Lock()
	ch, waiting := t.pending[env.ID]
	t.pendingMu.Unlock()

	if !waiting {
		// A reply for a request that already timed out, or a duplicate. Dropping it
		// is correct; keeping the entry alive "just in case" is exactly the leak the
		// unconditional delete above avoids.
		return
	}

	// Non-blocking on a buffer of one: a duplicate reply must not park the reader.
	select {
	case ch <- env:
	default:
	}
}
