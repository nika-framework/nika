package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nika-framework/nika/common/microservice"
)

// Listen declares the topology, consumes the service queue and blocks until ctx
// is cancelled or the connection fails.
//
// A failure is returned, never swallowed. An AMQP connection drops as a matter of
// routine — a broker upgrade, a network blip, a `rabbitmqctl close_connection` —
// and the core's listener supervisor already reconnects with exponential backoff.
// Retrying in here as well would produce two competing backoff loops and hide the
// failure from the supervisor's logs and metrics.
func (t *Transport) Listen(ctx context.Context, patterns []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("rabbitmq: Listen needs a dispatcher")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	// Translate before touching the network: a pattern AMQP cannot express is a
	// programming error and should not be reported as a broker problem.
	plan, err := planBindings(patterns)
	if err != nil {
		return err
	}

	conn, err := t.connection()
	if err != nil {
		return err
	}

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("rabbitmq: open consumer channel: %w", err)
	}
	// Deferred first so it runs last: in-flight handlers still need this channel
	// to ack and to publish their replies.
	defer func() { _ = ch.Close() }()

	deliveries, err := t.setup(ch, plan)
	if err != nil {
		return err
	}

	// Both notifications are registered before the loop starts so a failure during
	// setup-to-loop cannot be missed.
	chClosed := ch.NotifyClose(make(chan *amqp.Error, 1))
	connClosed := conn.NotifyClose(make(chan *amqp.Error, 1))

	var wg sync.WaitGroup
	defer wg.Wait()

	// The core dispatcher enforces the global concurrency cap and the per-handler
	// timeout; this semaphore exists only to keep the number of goroutines this
	// transport spawns proportional to its own Prefetch rather than to the queue
	// depth.
	slots := make(chan struct{}, t.opts.Concurrency)

	t.log.Info("rabbitmq consumer started",
		slog.String("exchange", t.opts.Exchange),
		slog.String("queue", t.opts.Queue),
		slog.Any("bindings", plan.Keys),
		slog.Int("prefetch", t.opts.Prefetch))

	if plan.CatchAll {
		t.log.Warn("rabbitmq: a character-level wildcard pattern forced a '#' binding; "+
			"this queue now receives every message on the exchange and filters in-process",
			slog.String("exchange", t.opts.Exchange),
			slog.String("queue", t.opts.Queue))
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-t.closed:
			return microservice.ErrClosed

		case err := <-chClosed:
			return fmt.Errorf("rabbitmq: consumer channel closed: %w", amqpErr(err))

		case err := <-connClosed:
			return fmt.Errorf("rabbitmq: connection closed: %w", amqpErr(err))

		case delivery, ok := <-deliveries:
			if !ok {
				// The broker cancelled the consumer (queue deleted, node failover)
				// or the channel went away. Either way this Listen is finished and
				// the supervisor should rebuild it.
				return errors.New("rabbitmq: the broker stopped delivering to this consumer")
			}

			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				// Nack rather than drop: the message has not been handled, and
				// requeueing it here is safe because no handler ran.
				if !t.opts.AutoAck {
					_ = delivery.Nack(false, true)
				}
				return nil
			}

			wg.Add(1)
			go func(d amqp.Delivery) {
				defer wg.Done()
				defer func() { <-slots }()
				t.handle(ctx, ch, d, dispatch)
			}(delivery)
		}
	}
}

// setup declares the exchange and the queue, binds the plan and applies QoS.
func (t *Transport) setup(ch *amqp.Channel, plan bindingPlan) (<-chan amqp.Delivery, error) {
	if err := t.declareExchange(ch); err != nil {
		return nil, err
	}

	if _, err := ch.QueueDeclare(t.opts.Queue, t.durable(), t.opts.AutoDelete,
		false /* exclusive */, false /* noWait */, t.queueArgs()); err != nil {
		return nil, fmt.Errorf("rabbitmq: declare queue %q: %w", t.opts.Queue, err)
	}

	for _, key := range plan.Keys {
		if err := ch.QueueBind(t.opts.Queue, key, t.opts.Exchange, false, nil); err != nil {
			return nil, fmt.Errorf("rabbitmq: bind %q to %q with key %q: %w",
				t.opts.Queue, t.opts.Exchange, key, err)
		}
	}

	// global=false applies the count per consumer, which is what balances work
	// across replicas. global=true would share one budget across the connection.
	if err := ch.Qos(t.opts.Prefetch, 0, false); err != nil {
		return nil, fmt.Errorf("rabbitmq: set prefetch to %d: %w", t.opts.Prefetch, err)
	}

	deliveries, err := ch.Consume(t.opts.Queue, "" /* generated tag */, t.opts.AutoAck,
		false /* exclusive */, false /* noLocal */, false /* noWait */, nil)
	if err != nil {
		return nil, fmt.Errorf("rabbitmq: consume queue %q: %w", t.opts.Queue, err)
	}
	return deliveries, nil
}

// handle runs one delivery through the dispatcher and settles it.
func (t *Transport) handle(ctx context.Context, ch *amqp.Channel, delivery amqp.Delivery, dispatch microservice.Dispatcher) {
	env, err := microservice.DecodeEnvelope(delivery.Body)
	if err != nil {
		// Skip, never die: one bad publisher must not be able to stop a consumer.
		// Rejected without requeue whatever Options.Requeue says, because bytes
		// that do not parse now will not parse on redelivery either — that is a
		// guaranteed infinite loop, not a retry.
		t.log.Warn("rabbitmq: dropping malformed message",
			slog.String("routing_key", delivery.RoutingKey),
			slog.Int("bytes", len(delivery.Body)),
			slog.Any("error", err))
		t.settle(delivery, false, false)
		return
	}

	// Prefer the AMQP property: a relay may have rewritten it, and it is the one
	// the broker itself would honour.
	replyTo := delivery.ReplyTo
	if replyTo == "" {
		replyTo = env.ReplyTo
	}

	reply, err := dispatch(ctx, env)
	if err != nil {
		// Per the Dispatcher contract an error return means the message itself was
		// unusable, so it is nacked. Requeue only if the operator asked for it.
		t.log.Warn("rabbitmq: dispatch rejected the message",
			slog.String("pattern", env.Pattern),
			slog.Bool("requeue", t.opts.Requeue),
			slog.Any("error", err))
		t.settle(delivery, false, t.opts.Requeue)
		return
	}

	if replyTo == "" {
		// Fire-and-forget: there is nowhere to send a reply, so the dispatcher's
		// result is discarded. This is the normal shape of an event.
		t.settle(delivery, true, false)
		return
	}

	if reply == nil {
		// A handler that produced nothing still owes the caller an answer;
		// otherwise the caller waits out its whole timeout for no reason.
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

	// The reply is published before the ack, so a crash between the two redelivers
	// the request rather than losing the answer.
	if err := t.publishReply(ctx, ch, replyTo, reply); err != nil {
		t.log.Error("rabbitmq: could not deliver reply",
			slog.String("pattern", env.Pattern),
			slog.String("reply_to", replyTo),
			slog.Any("error", err))
		// Still ack. The handler has already run and may have written to a
		// database; requeueing would run it a second time to fix a problem that
		// only affects the reply path.
	}

	t.settle(delivery, true, false)
}

// settle acks or nacks a delivery, unless AutoAck already did.
func (t *Transport) settle(delivery amqp.Delivery, ack, requeue bool) {
	if t.opts.AutoAck {
		return
	}

	var err error
	if ack {
		err = delivery.Ack(false)
	} else {
		err = delivery.Nack(false, requeue)
	}
	if err != nil && !isClosedErr(err) {
		// A failed ack means the channel died; the message will be redelivered.
		// Nothing to do but record it.
		t.log.Warn("rabbitmq: could not settle delivery",
			slog.Uint64("delivery_tag", delivery.DeliveryTag),
			slog.Bool("ack", ack),
			slog.Any("error", err))
	}
}

// amqpErr turns a possibly-nil *amqp.Error into a non-nil error, so wrapping it
// never produces "closed: %!w(<nil>)".
func amqpErr(err *amqp.Error) error {
	if err == nil {
		return errors.New("closed by the peer without a reason")
	}
	return err
}
