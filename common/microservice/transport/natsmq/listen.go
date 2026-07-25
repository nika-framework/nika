package natsmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/nika-framework/nika/common/microservice"
)

// pendingLimits raise the per-subscription buffer NATS keeps for messages that have
// been received but not yet handed to a callback. The library default (~512k
// messages / 64 MiB) is generous, but a burst against a saturated Concurrency cap
// can still reach it, and reaching it means NATS *drops* messages and reports a
// slow consumer. Setting them explicitly makes the limit a deliberate choice rather
// than an accident.
const (
	pendingMsgLimit   = 256 * 1024
	pendingBytesLimit = 64 << 20 // 64 MiB
)

// Listen subscribes to the handler patterns and dispatches until ctx is cancelled
// or the transport is closed.
func (t *Transport) Listen(ctx context.Context, patterns []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("natsmq: a dispatcher is required")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	subjects, catchAll, err := subjectPlan(patterns)
	if err != nil {
		return err
	}

	nc, err := t.conn()
	if err != nil {
		return err
	}

	slots := make(chan struct{}, t.concurrency)
	handler := func(msg *nats.Msg) {
		// The callback must return promptly: NATS invokes callbacks for one
		// subscription serially, so doing the work here would serialise every
		// handler behind the slowest one. The semaphore is what stops that
		// hand-off from becoming an unbounded goroutine fan-out.
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		case <-t.closed:
			return
		}

		t.handlers.Add(1)
		go func() {
			defer t.handlers.Done()
			defer func() { <-slots }()
			t.handleMessage(ctx, nc, msg, dispatch)
		}()
	}

	var subs []*nats.Subscription
	subscribe := func(subject string) error {
		var (
			sub *nats.Subscription
			err error
		)
		if t.queueGroup != "" {
			sub, err = nc.QueueSubscribe(subject, t.queueGroup, handler)
		} else {
			sub, err = nc.Subscribe(subject, handler)
		}
		if err != nil {
			return fmt.Errorf("natsmq: subscribe %q: %w", subject, err)
		}
		if limitErr := sub.SetPendingLimits(pendingMsgLimit, pendingBytesLimit); limitErr != nil {
			t.log.Warn("natsmq cannot raise pending limits",
				slog.String("subject", subject),
				slog.Any("error", limitErr),
			)
		}
		subs = append(subs, sub)
		return nil
	}

	unsubscribeAll := func() {
		for _, sub := range subs {
			// Drain, not Unsubscribe: it stops new deliveries but still runs the
			// callbacks for messages already buffered, so a shutdown does not throw
			// away messages the broker considers delivered.
			if err := sub.Drain(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
				t.log.Warn("natsmq cannot drain subscription",
					slog.String("subject", sub.Subject),
					slog.Any("error", err),
				)
			}
		}
	}

	if catchAll {
		// Exactly one subscription. Adding the literal subjects as well would
		// double-deliver every literal message, because `>` already matches them
		// and NATS delivers once per matching subscription.
		if err := subscribe(catchAllSubject(t.prefix)); err != nil {
			return err
		}
		t.log.Warn("natsmq subscribed to the prefix catch-all because a wildcard pattern is registered; "+
			"this process now receives every message under the prefix and filters locally",
			slog.String("subject", catchAllSubject(t.prefix)),
		)
	} else {
		for _, subject := range subjects {
			if err := subscribe(joinSubject(t.prefix, subject)); err != nil {
				unsubscribeAll()
				return err
			}
		}
	}

	// Flush so the SUB commands have reached the server before Listen is considered
	// live. Without it a message published immediately after startup can arrive at
	// the server before the subscription does and be dropped.
	if err := nc.FlushWithContext(ctx); err != nil {
		unsubscribeAll()
		return fmt.Errorf("natsmq: flushing subscriptions: %w", mapNATSError(err))
	}

	t.log.Info("natsmq listening",
		slog.Int("subjects", len(subs)),
		slog.Bool("catch_all", catchAll),
		slog.String("prefix", t.prefix),
		slog.String("queue_group", t.queueGroup),
	)

	defer unsubscribeAll()

	select {
	case <-ctx.Done():
		return nil
	case <-t.closed:
		return microservice.ErrClosed
	}
}

// handleMessage dispatches one NATS message and answers it when a reply was asked
// for.
func (t *Transport) handleMessage(ctx context.Context, nc *nats.Conn, msg *nats.Msg, dispatch microservice.Dispatcher) {
	env, err := microservice.DecodeEnvelope(msg.Data)
	if err != nil {
		// Log and skip: one publisher writing garbage onto a shared subject must
		// not be able to stop this consumer. If a reply was expected the caller
		// gets ErrTimeout, which is the honest outcome for a message we could not
		// even parse.
		t.log.Warn("natsmq dropping undecodable message",
			slog.String("subject", msg.Subject),
			slog.Int("bytes", len(msg.Data)),
			slog.Any("error", err),
		)
		return
	}

	// The reply address on NATS lives in the protocol, not in the envelope: a
	// native request carries a server-generated inbox in msg.Reply. Copy it onto
	// the envelope so handlers and middleware see one consistent notion of "a reply
	// is expected", and fall back to an explicit envelope ReplyTo for a publisher
	// that set one by hand.
	replySubject := msg.Reply
	if replySubject == "" {
		replySubject = env.ReplyTo
	}
	env.ReplyTo = replySubject

	reply, dispatchErr := dispatch(ctx, env)

	if replySubject == "" {
		// Fire and forget: dispatch for the side effects and discard the reply.
		if dispatchErr != nil {
			t.log.Warn("natsmq dispatch failed",
				slog.String("pattern", env.Pattern),
				slog.Any("error", dispatchErr),
			)
		}
		return
	}

	if reply == nil {
		detail := "handler produced no reply"
		if dispatchErr != nil {
			detail = dispatchErr.Error()
		}
		reply = errorReply(env, 500, "DISPATCH_ERROR", detail)
	}
	// The correlation id always comes from the request; a handler must not be able
	// to point its reply somewhere else.
	reply.ID = env.ID

	payload, encodeErr := reply.Encode()
	if encodeErr != nil {
		t.log.Error("natsmq cannot encode reply",
			slog.String("pattern", env.Pattern),
			slog.Any("error", encodeErr),
		)
		payload, encodeErr = errorReply(env, 500, "ENCODE_ERROR", "reply could not be encoded").Encode()
		if encodeErr != nil {
			return
		}
	}

	// Respond when the reply address came from the protocol, so NATS routes it back
	// through the requester's inbox; publish explicitly when a publisher supplied
	// its own reply subject.
	var respondErr error
	if msg.Reply != "" {
		respondErr = msg.Respond(payload)
	} else {
		respondErr = nc.Publish(replySubject, payload)
	}

	if respondErr != nil {
		// nats.ErrMaxPayload here means the handler produced a reply larger than
		// the server's max_payload; the caller will time out and the fix is either a
		// smaller response or a larger server limit.
		t.log.Warn("natsmq cannot send reply",
			slog.String("pattern", env.Pattern),
			slog.String("reply_to", replySubject),
			slog.Any("error", respondErr),
		)
	}
}
