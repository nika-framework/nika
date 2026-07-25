package kafkamq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/segmentio/kafka-go"

	"github.com/nika-framework/nika/common/microservice"
)

// Listen joins the consumer group, consumes the topic and blocks until ctx is
// cancelled or the reader fails fatally.
//
// patterns are not used for broker-side filtering: there is one topic and Kafka
// has no subject subscriptions, so every message reaches the dispatcher and the
// core Router does the matching. They are logged so a misconfiguration is visible.
//
// A fatal error is returned rather than retried in place — the core's listener
// supervisor already reconnects with exponential backoff, and a second backoff
// loop in here would hide the failure from its logs.
func (t *Transport) Listen(ctx context.Context, patterns []string, dispatch microservice.Dispatcher) error {
	if dispatch == nil {
		return errors.New("kafkamq: Listen needs a dispatcher")
	}
	if t.opts.GroupID == "" {
		return errors.New(
			"kafkamq: Options.GroupID is required to Listen — without a consumer group every replica " +
				"of this service consumes every message instead of sharing the work")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	ctx, cancel := t.linkClose(ctx)
	defer cancel()

	if t.opts.CreateTopics {
		if err := t.createTopics(ctx, t.opts.Topic, t.opts.ReplyTopic); err != nil {
			return err
		}
	}

	reader := kafka.NewReader(t.readerConfig(t.opts.Topic, t.opts.GroupID))
	// Deferred first so it runs last: in-flight handlers still need the reader to
	// commit their offsets.
	defer func() { _ = reader.Close() }()

	var wg sync.WaitGroup
	defer wg.Wait()

	// The core dispatcher already enforces the global concurrency cap and the
	// per-handler timeout. This semaphore exists for the ordering and commit
	// semantics described on Options.Concurrency, which are specific to Kafka.
	slots := make(chan struct{}, t.opts.Concurrency)

	t.log.Info("kafka consumer started",
		slog.String("topic", t.opts.Topic),
		slog.String("group", t.opts.GroupID),
		slog.Int("concurrency", t.opts.Concurrency),
		slog.Any("patterns", patterns))

	if t.opts.Concurrency > 1 {
		t.log.Warn("kafkamq: Concurrency above 1 gives up per-partition ordering and commits "+
			"offsets out of order; set Concurrency to 1 when ordering matters",
			slog.Int("concurrency", t.opts.Concurrency))
	}

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			switch {
			case t.isClosed():
				return microservice.ErrClosed
			case ctx.Err() != nil:
				// Cancellation is a clean shutdown, not a failure to retry.
				return nil
			default:
				return fmt.Errorf("kafkamq: fetching from %s: %w", t.opts.Topic, err)
			}
		}

		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			// Deliberately not committed: the message was never handled, so leaving
			// the offset alone is what makes the next start redeliver it.
			return nil
		}

		wg.Add(1)
		go func(m kafka.Message) {
			defer wg.Done()
			defer func() { <-slots }()
			t.handle(ctx, reader, m, dispatch)
		}(msg)
	}
}

// handle runs one message through the dispatcher, replies if asked to, and
// commits.
//
// On a handler failure the offset is committed anyway. That is a decision, not an
// oversight:
//
//   - Kafka tracks one offset watermark per partition, so "do not commit this
//     one" is not expressible. Skipping the commit redelivers the entire
//     uncommitted range after it on the next start, re-running every message that
//     already succeeded.
//   - A message that fails deterministically would then block its partition
//     forever, and the partition is the unit of throughput: one bad message stalls
//     every subject that hashes to it.
//
// So the failure is logged, the caller gets an error reply when it asked for one,
// and the stream moves on. A dead-letter topic — republish the raw message to
// Topic+".dlq" and commit — is the right escalation when losing the message is not
// acceptable; it belongs in the handler or a middleware, where the payload's
// semantics are known.
func (t *Transport) handle(ctx context.Context, reader *kafka.Reader, msg kafka.Message, dispatch microservice.Dispatcher) {
	env, err := microservice.DecodeEnvelope(msg.Value)
	if err != nil {
		// Skip, never die: one bad producer must not stop the consumer. Committed,
		// because bytes that do not parse now will not parse on redelivery.
		t.log.Warn("kafkamq: dropping malformed message",
			slog.String("topic", msg.Topic),
			slog.Int("partition", msg.Partition),
			slog.Int64("offset", msg.Offset),
			slog.Int("bytes", len(msg.Value)),
			slog.Any("error", err))
		t.commit(ctx, reader, msg)
		return
	}

	// Prefer the header: a relay can rewrite it without re-encoding the payload.
	replyTo := header(msg, headerReplyTo)
	if replyTo == "" {
		replyTo = env.ReplyTo
	}

	reply, err := dispatch(ctx, env)
	if err != nil {
		t.log.Warn("kafkamq: dispatch rejected the message; committing and moving on",
			slog.String("pattern", env.Pattern),
			slog.Int("partition", msg.Partition),
			slog.Int64("offset", msg.Offset),
			slog.Any("error", err))
		t.commit(ctx, reader, msg)
		return
	}

	if replyTo == "" {
		// Fire-and-forget: no reply address, so the dispatcher's result is
		// discarded. This is the normal shape of an event on Kafka.
		t.commit(ctx, reader, msg)
		return
	}

	if reply == nil {
		// A handler that produced nothing still owes the caller an answer, or the
		// caller waits out its whole timeout for no reason.
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
	reply.ReplyTo = ""

	// The reply is produced before the commit, so a crash between the two
	// redelivers the request rather than losing the answer.
	if replyMsg, err := t.message(replyTo, reply); err != nil {
		t.log.Error("kafkamq: could not encode the reply", slog.String("pattern", env.Pattern), slog.Any("error", err))
	} else if err := t.write(ctx, replyMsg); err != nil {
		t.log.Error("kafkamq: could not produce the reply",
			slog.String("pattern", env.Pattern),
			slog.String("reply_topic", replyTo),
			slog.Any("error", err))
	}

	t.commit(ctx, reader, msg)
}

// commit advances the group offset past msg.
func (t *Transport) commit(ctx context.Context, reader *kafka.Reader, msg kafka.Message) {
	// A cancelled context would make the commit fail immediately, which during a
	// graceful shutdown would needlessly redeliver work that is already done.
	// context.WithoutCancel keeps the values and drops the cancellation; the
	// reader's own Close still bounds it.
	if err := reader.CommitMessages(context.WithoutCancel(ctx), msg); err != nil {
		if t.isClosed() || errors.Is(err, context.Canceled) {
			return
		}
		// Not fatal: the offset stays where it was, so the message is redelivered.
		// At-least-once is doing exactly what it promises.
		t.log.Warn("kafkamq: committing an offset failed; the message will be redelivered",
			slog.String("topic", msg.Topic),
			slog.Int("partition", msg.Partition),
			slog.Int64("offset", msg.Offset),
			slog.Any("error", err))
	}
}

// header returns a Kafka header value, or "" when absent.
func header(msg kafka.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
