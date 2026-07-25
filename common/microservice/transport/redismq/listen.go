package redismq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nika-framework/nika/common/microservice"
)

// Listen subscribes to the handler patterns and dispatches until ctx is cancelled
// or the transport is closed.
//
// Wildcard patterns are translated to Redis globs and PSUBSCRIBEd rather than
// filtered in-process, so the broker only sends this consumer messages it can
// actually handle. The alternative — subscribing to one wide channel and matching
// locally — would put every message published by every service on this instance
// through this process's socket, decoder and CPU.
func (t *Transport) Listen(ctx context.Context, patterns []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("redismq: a dispatcher is required")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	subPlan, err := newPlan(t.prefix, patterns)
	if err != nil {
		return err
	}

	// The empty Subscribe is deliberate: (*redis.Client).Subscribe discards the
	// error from its initial SUBSCRIBE, so a channel name Redis rejects would look
	// like a healthy subscription that silently never receives anything. Creating an
	// empty PubSub and issuing the subscriptions through it surfaces the error.
	sub := t.client.Subscribe(ctx)

	if len(subPlan.channels) > 0 {
		if err := sub.Subscribe(ctx, subPlan.channels...); err != nil {
			_ = sub.Close()
			return fmt.Errorf("redismq: subscribe %v: %w", subPlan.channels, err)
		}
	}
	if len(subPlan.globs) > 0 {
		globs := make([]string, len(subPlan.globs))
		for i, g := range subPlan.globs {
			globs[i] = g.glob
		}
		if err := sub.PSubscribe(ctx, globs...); err != nil {
			_ = sub.Close()
			return fmt.Errorf("redismq: psubscribe %v: %w", globs, err)
		}
	}

	// Receive blocks for the server's subscription confirmation, which is the only
	// proof the subscription actually exists. It must be called before Channel,
	// because Channel starts the background receiver and the Receive family cannot
	// be used afterwards.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return fmt.Errorf("redismq: confirming subscription: %w", mapTimeout(err))
	}

	messages := sub.Channel()

	t.log.Info("redismq listening",
		slog.Int("channels", len(subPlan.channels)),
		slog.Int("patterns", len(subPlan.globs)),
		slog.String("prefix", t.prefix),
	)

	// Handlers must finish before the subscription is torn down, so the WaitGroup
	// is deferred after (and therefore runs before) the Close.
	defer func() { _ = sub.Close() }()

	var handlers sync.WaitGroup
	defer handlers.Wait()

	slots := make(chan struct{}, t.concurrency)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.closed:
			return microservice.ErrClosed

		case msg, open := <-messages:
			if !open {
				// go-redis reconnects and resubscribes on its own, so a closed
				// channel means the PubSub itself was closed. During shutdown that
				// is expected; otherwise report it so the supervisor rebuilds the
				// subscription with backoff.
				if ctx.Err() != nil {
					return nil
				}
				if t.isClosed() {
					return microservice.ErrClosed
				}
				return errors.New("redismq: subscription closed unexpectedly")
			}

			if !subPlan.accept(msg.Pattern, msg.Channel) {
				// Already delivered through a more specific subscription; see
				// plan.accept for why collapsing this matters.
				continue
			}

			env, decodeErr := microservice.DecodeEnvelope([]byte(msg.Payload))
			if decodeErr != nil {
				// Log and skip. One publisher writing garbage — a different
				// service on the same prefix, a stray redis-cli PUBLISH — must not
				// be able to tear down a subscription serving everyone else.
				t.log.Warn("redismq dropping undecodable message",
					slog.String("channel", msg.Channel),
					slog.Int("bytes", len(msg.Payload)),
					slog.Any("error", decodeErr),
				)
				continue
			}

			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return nil
			case <-t.closed:
				return microservice.ErrClosed
			}

			// Each message is dispatched in its own goroutine: the subscription is a
			// single stream, and handling inline would let one slow handler stall
			// every other pattern this consumer serves.
			handlers.Add(1)
			go func(env *microservice.Envelope) {
				defer handlers.Done()
				defer func() { <-slots }()
				t.handleMessage(ctx, env, dispatch)
			}(env)
		}
	}
}

// handleMessage dispatches one envelope and publishes the reply when one was asked
// for.
func (t *Transport) handleMessage(ctx context.Context, env *microservice.Envelope, dispatch microservice.Dispatcher) {
	reply, err := dispatch(ctx, env)

	// An empty ReplyTo is a fire-and-forget event: run it for its side effects and
	// discard the reply rather than publishing to a channel nobody is reading.
	if env.ReplyTo == "" {
		if err != nil {
			t.log.Warn("redismq dispatch failed",
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
	// to steer its reply into another caller's pending entry.
	reply.ID = env.ID

	payload, encodeErr := reply.Encode()
	if encodeErr != nil {
		t.log.Error("redismq cannot encode reply",
			slog.String("pattern", env.Pattern),
			slog.Any("error", encodeErr),
		)
		payload, encodeErr = errorReply(env, 500, "ENCODE_ERROR", "reply could not be encoded").Encode()
		if encodeErr != nil {
			return
		}
	}

	// Detach from ctx before publishing. During shutdown ctx is already cancelled,
	// but the handler ran to completion and the caller is still waiting — sending
	// the answer is strictly better than making it time out. The deadline keeps the
	// goroutine bounded.
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.replyTimeout)
	defer cancel()

	if err := t.client.Publish(publishCtx, env.ReplyTo, payload).Err(); err != nil {
		t.log.Warn("redismq cannot publish reply",
			slog.String("pattern", env.Pattern),
			slog.String("reply_to", env.ReplyTo),
			slog.Any("error", err),
		)
	}
}
