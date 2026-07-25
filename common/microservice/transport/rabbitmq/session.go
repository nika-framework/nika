package rabbitmq

import (
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nika-framework/nika/common/microservice"
)

// session is the client-side half of the transport on one live connection: a
// publish channel in confirm mode, plus this process's exclusive reply queue and
// the single consumer that demultiplexes replies into Transport.pending.
//
// It is a value rather than fields on Transport because an AMQP connection drops
// routinely, and everything derived from it dies together. Bundling them means a
// drop is handled by throwing one object away instead of by resetting six fields
// consistently under a lock.
type session struct {
	ch         *amqp.Channel
	replyQueue string

	// dead closes when the channel or the connection underneath it fails, so a
	// Request already waiting for a reply fails fast instead of waiting out its
	// full timeout for a reply that can no longer arrive.
	dead chan struct{}
}

func (s *session) isDead() bool {
	select {
	case <-s.dead:
		return true
	default:
		return s.ch.IsClosed()
	}
}

func (s *session) close() error { return s.ch.Close() }

// session returns a usable client session, building one if the current one is
// missing or dead.
//
// The whole function holds t.mu, including the dial. That serialises reconnects,
// which is the point: a hundred concurrent publishes hitting a dead connection
// should produce one reconnect, not a hundred. The established path only reads
// two fields under the lock, so the steady state is uncontended.
func (t *Transport) session() (*session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isClosed() {
		return nil, microservice.ErrClosed
	}
	if t.sess != nil && !t.sess.isDead() {
		return t.sess, nil
	}
	if t.sess != nil {
		_ = t.sess.close()
		t.sess = nil
		// Every request waiting on the dead session has been released by its own
		// dead channel; drop their correlation entries so a session churn cannot
		// accumulate them.
		t.failPending()
	}

	conn, err := t.connectionLocked()
	if err != nil {
		return nil, err
	}

	sess, err := t.newSession(conn)
	if err != nil {
		return nil, err
	}
	t.sess = sess
	return sess, nil
}

// newSession opens the publish channel, declares the reply queue and starts the
// reply consumer. The consumer is running before this returns, which is what lets
// Request publish without racing its own reply — a fast peer can answer before
// the publish call has returned.
func (t *Transport) newSession(conn *amqp.Connection) (*session, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: open publish channel: %w", err)
	}

	if err := t.declareExchange(ch); err != nil {
		_ = ch.Close()
		return nil, err
	}

	if !t.opts.DisableConfirms {
		if err := ch.Confirm(false); err != nil {
			_ = ch.Close()
			return nil, fmt.Errorf("rabbitmq: enable publisher confirms: %w", err)
		}
	}

	// An empty name asks the broker to generate one. This is the one place an
	// exclusive auto-delete queue is correct: a reply is only useful to the
	// process that is still waiting for it, so a reply queue outliving its client
	// would just accumulate garbage.
	replyQ, err := ch.QueueDeclare("", false /* durable */, true, /* autoDelete */
		true /* exclusive */, false /* noWait */, nil)
	if err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("rabbitmq: declare reply queue: %w", err)
	}

	// autoAck on the reply queue: the queue is exclusive to this process and its
	// contents are worthless if we die, so there is nothing for an ack to protect
	// and one round trip per reply to save.
	replies, err := ch.Consume(replyQ.Name, "", true /* autoAck */, true, /* exclusive */
		false /* noLocal */, false /* noWait */, nil)
	if err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("rabbitmq: consume reply queue %q: %w", replyQ.Name, err)
	}

	sess := &session{
		ch:         ch,
		replyQueue: replyQ.Name,
		dead:       make(chan struct{}),
	}

	// One watcher per session, terminated by the channel's own close
	// notification, which amqp091-go also fires when the connection dies.
	notify := ch.NotifyClose(make(chan *amqp.Error, 1))
	go t.watchSession(sess, notify)

	if t.opts.Mandatory {
		go drainReturns(t.log, ch.NotifyReturn(make(chan amqp.Return, 8)))
	}

	go t.consumeReplies(sess, replies)

	return sess, nil
}

// watchSession closes sess.dead when the channel fails, then releases every
// waiter. It exits when the notification channel is closed by amqp091-go, so it
// cannot outlive its session.
func (t *Transport) watchSession(sess *session, notify chan *amqp.Error) {
	err, ok := <-notify
	close(sess.dead)
	if ok && err != nil && !t.isClosed() {
		t.log.Warn("rabbitmq publish channel closed; will reconnect on next publish",
			slog.Any("error", err))
	}
	t.failPending()
}

// consumeReplies demultiplexes the reply queue into the pending map. It exits
// when the broker closes the delivery channel or the transport closes, so it
// cannot leak past either.
func (t *Transport) consumeReplies(sess *session, replies <-chan amqp.Delivery) {
	for {
		select {
		case <-t.closed:
			return
		case <-sess.dead:
			return
		case delivery, ok := <-replies:
			if !ok {
				return
			}
			t.deliverReply(delivery)
		}
	}
}

// deliverReply routes one reply to the Request waiting for it.
func (t *Transport) deliverReply(delivery amqp.Delivery) {
	env, err := microservice.DecodeEnvelope(delivery.Body)
	if err != nil {
		// A reply we cannot parse is a bug on the far side, not a reason to stop
		// serving every other in-flight request. The waiting Request times out.
		t.log.Warn("rabbitmq: discarding malformed reply",
			slog.String("correlation_id", delivery.CorrelationId),
			slog.Any("error", err))
		return
	}

	// CorrelationId is the AMQP-native place for this; env.ID is the fallback for
	// a peer that only filled in the envelope.
	id := delivery.CorrelationId
	if id == "" {
		id = env.ID
	}

	t.pendingMu.Lock()
	waiter, found := t.pending[id]
	// Deleting here as well as in Request's defer means this send can never be
	// the second one on the channel, so the buffer of 1 is always free.
	delete(t.pending, id)
	t.pendingMu.Unlock()

	if !found {
		// Almost always a reply that arrived after its Request timed out. Logged
		// at debug because under load it is normal, not an incident.
		t.log.Debug("rabbitmq: reply has no waiting request",
			slog.String("id", id), slog.String("pattern", env.Pattern))
		return
	}

	waiter <- env
}

// failPending drops every correlation entry. The waiters are released by their
// own select on session.dead or Transport.closed; this only stops the map from
// growing across reconnects.
func (t *Transport) failPending() {
	t.pendingMu.Lock()
	if len(t.pending) > 0 {
		t.pending = make(map[string]chan *microservice.Envelope)
	}
	t.pendingMu.Unlock()
}

// drainReturns logs unroutable messages. Draining the channel is not optional:
// amqp091-go blocks the connection's read loop when a NotifyReturn listener stops
// consuming, so an unread returns channel wedges the whole connection.
func drainReturns(log *slog.Logger, returns <-chan amqp.Return) {
	for ret := range returns {
		log.Warn("rabbitmq: message was unroutable and returned by the broker",
			slog.String("routing_key", ret.RoutingKey),
			slog.String("message_id", ret.MessageId),
			slog.String("reason", ret.ReplyText))
	}
}
