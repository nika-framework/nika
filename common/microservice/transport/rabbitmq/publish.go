package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nika-framework/nika/common/microservice"
)

// Publish sends an envelope to the topic exchange and does not wait for a reply.
//
// "Does not wait for a reply" is not the same as "does not wait": with publisher
// confirms on — the default — Publish waits for the broker to acknowledge the
// message. That is the difference between an event that was published and one
// that was merely written to a socket.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("rabbitmq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	key, err := toRoutingKey(env.Pattern)
	if err != nil {
		return err
	}
	body, err := env.Encode()
	if err != nil {
		return fmt.Errorf("rabbitmq: encode envelope: %w", err)
	}

	sess, err := t.session()
	if err != nil {
		return err
	}

	return t.publish(ctx, sess, t.opts.Exchange, key, amqp.Publishing{
		ContentType:   "application/json",
		DeliveryMode:  amqp.Persistent,
		MessageId:     env.ID,
		CorrelationId: env.ID,
		ReplyTo:       env.ReplyTo,
		Timestamp:     time.Now().UTC(),
		Body:          body,
	})
}

// Request publishes an envelope with a reply address and waits for the correlated
// reply.
//
// This is the standard AMQP RPC pattern: one long-lived exclusive reply queue per
// client process, correlation by CorrelationId, and a single consumer
// demultiplexing into a map of waiters. A reply queue per call would cost a queue
// declaration round trip on every request and leave a trail of queues behind
// whenever a client died mid-call.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("rabbitmq: cannot request with a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}

	key, err := toRoutingKey(env.Pattern)
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = t.opts.ReplyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The session is established — reply queue declared, reply consumer running —
	// before anything is published, so a peer that answers instantly cannot beat
	// us to our own mailbox.
	sess, err := t.session()
	if err != nil {
		return nil, err
	}

	id := env.ID
	if id == "" {
		id = microservice.NewID()
	}

	replies, release := t.registerPending(id)
	defer release()

	// Copy rather than mutate: the caller may reuse or inspect its envelope, and
	// overwriting ReplyTo with a broker-generated queue name would surprise it.
	out := *env
	out.ID = id
	out.ReplyTo = sess.replyQueue
	body, err := out.Encode()
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: encode envelope: %w", err)
	}

	err = t.publish(ctx, sess, t.opts.Exchange, key, amqp.Publishing{
		ContentType:   "application/json",
		DeliveryMode:  amqp.Persistent,
		MessageId:     id,
		CorrelationId: id,
		ReplyTo:       sess.replyQueue,
		// Expiration makes the broker drop the request when the caller's deadline
		// has passed, so a queue that is backed up does not hand a consumer work
		// nobody is listening for any more.
		Expiration: fmt.Sprintf("%d", timeout.Milliseconds()),
		Timestamp:  time.Now().UTC(),
		Body:       body,
	})
	if err != nil {
		return nil, err
	}

	return t.awaitReply(ctx, id, replies, sess.dead)
}

// registerPending reserves a correlation slot and returns the release function
// the caller must defer.
//
// The reply channel is buffered so the reply consumer's send cannot block, and
// release is idempotent with the delete the consumer performs, so the entry
// disappears on every path: reply, timeout, cancellation, connection loss, close.
func (t *Transport) registerPending(id string) (<-chan *microservice.Envelope, func()) {
	replies := make(chan *microservice.Envelope, 1)

	t.pendingMu.Lock()
	t.pending[id] = replies
	t.pendingMu.Unlock()

	return replies, func() {
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
	}
}

// awaitReply blocks for the correlated reply, whichever of the caller's context,
// the request timeout, the connection or the transport gives up first.
func (t *Transport) awaitReply(
	ctx context.Context,
	id string,
	replies <-chan *microservice.Envelope,
	dead <-chan struct{},
) (*microservice.Envelope, error) {
	select {
	case reply := <-replies:
		return reply, nil
	case <-dead:
		return nil, fmt.Errorf("rabbitmq: connection lost while awaiting the reply to %s", id)
	case <-t.closed:
		return nil, microservice.ErrClosed
	case <-ctx.Done():
		return nil, mapTimeout(ctx.Err())
	}
}

// publish writes one message and, when confirms are on, waits for the broker's
// verdict.
func (t *Transport) publish(ctx context.Context, sess *session, exchange, key string, msg amqp.Publishing) error {
	confirm, err := sess.ch.PublishWithDeferredConfirmWithContext(
		ctx, exchange, key, t.opts.Mandatory, false /* immediate */, msg)
	if err != nil {
		// A publish failure is almost always a dead channel. Marking the session
		// dead makes the next call reconnect instead of retrying on a corpse.
		if sess.ch.IsClosed() {
			t.discard(sess)
		}
		return fmt.Errorf("rabbitmq: publish to %s/%s: %w", exchange, key, mapTimeout(err))
	}
	if confirm == nil {
		// Confirms disabled: the frame is written and that is all we can know.
		return nil
	}

	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("rabbitmq: awaiting publisher confirm for %s/%s: %w", exchange, key, mapTimeout(err))
	}
	if !acked {
		// A nack means the broker took the message and then refused it — a full
		// disk, a failed quorum write. Silently returning success here is how
		// "we published it" and "the queue is empty" end up both being true.
		return fmt.Errorf("rabbitmq: broker rejected the publish to %s/%s", exchange, key)
	}
	return nil
}

// discard marks the current session unusable so the next call rebuilds it.
func (t *Transport) discard(sess *session) {
	t.mu.Lock()
	if t.sess == sess {
		t.sess = nil
	}
	t.mu.Unlock()
	_ = sess.close()
}

// publishReply sends a handler's reply straight to the requester's reply queue.
//
// It goes through the default exchange ("") with the queue name as the routing
// key, which is AMQP's built-in direct addressing. The queue name is deliberately
// not passed through toRoutingKey: broker-generated names look like
// "amq.gen-JzTY20BRgKO-HjmUJj0wLg" and contain the dots that a pattern is not
// allowed to.
func (t *Transport) publishReply(ctx context.Context, ch *amqp.Channel, queue string, reply *microservice.Envelope) error {
	body, err := reply.Encode()
	if err != nil {
		return fmt.Errorf("rabbitmq: encode reply: %w", err)
	}

	err = ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
		ContentType: "application/json",
		// Transient: a reply is only worth anything to a caller that is still
		// waiting, so paying for a disk write is pure latency.
		DeliveryMode:  amqp.Transient,
		MessageId:     reply.ID,
		CorrelationId: reply.ID,
		Timestamp:     time.Now().UTC(),
		Body:          body,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("rabbitmq: publish reply to %q: %w", queue, err)
	}
	return nil
}
