package kafkamq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/nika-framework/nika/common/microservice"
)

// Kafka header names. They duplicate fields that are already inside the JSON
// envelope, which is deliberate: a header is visible to kafka-console-consumer,
// to a Connect transform and to any monitoring that must not parse payloads.
const (
	headerPattern = "nika-pattern"
	headerID      = "nika-id"
	headerReplyTo = "nika-reply-to"
)

// Publish produces an envelope to the topic and does not wait for a consumer.
//
// This is Kafka's natural mode. The message is durable on the broker for the
// topic's retention period, so a consumer that is down when it is published still
// receives it when it comes back — the opposite of gRPC and the reason to put
// events here.
func (t *Transport) Publish(ctx context.Context, env *microservice.Envelope) error {
	if env == nil {
		return errors.New("kafkamq: cannot publish a nil envelope")
	}
	if t.isClosed() {
		return microservice.ErrClosed
	}

	msg, err := t.message(t.opts.Topic, env)
	if err != nil {
		return err
	}
	return t.write(ctx, msg)
}

// Request produces an envelope carrying a reply address and waits for the
// correlated reply on ReplyTopic.
//
// Read the package comment before reaching for this. It works, and it costs a
// produce round trip plus a fetch round trip plus — on the first call of a
// process — a consumer-group join, which is typically hundreds of milliseconds.
func (t *Transport) Request(ctx context.Context, env *microservice.Envelope, timeout time.Duration) (*microservice.Envelope, error) {
	if env == nil {
		return nil, errors.New("kafkamq: cannot request with a nil envelope")
	}
	if t.isClosed() {
		return nil, microservice.ErrClosed
	}
	if t.opts.ReplyTopic == "" {
		return nil, fmt.Errorf(
			"%w: Kafka request/reply needs Options.ReplyTopic — a topic has no built-in reply address, "+
				"so replies must be produced to a topic this client consumes",
			microservice.ErrNotSupported)
	}

	if timeout <= 0 {
		timeout = t.opts.ReplyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The reply consumer is joined and fetching before anything is produced, so a
	// fast handler cannot answer into a topic nobody is reading yet.
	if err := t.ensureReplyConsumer(); err != nil {
		return nil, err
	}

	id := env.ID
	if id == "" {
		id = microservice.NewID()
	}

	replies, release := t.registerPending(id)
	defer release()

	// Copy rather than mutate: the caller may reuse its envelope, and overwriting
	// ReplyTo would surprise it.
	out := *env
	out.ID = id
	out.ReplyTo = t.opts.ReplyTopic

	msg, err := t.message(t.opts.Topic, &out)
	if err != nil {
		return nil, err
	}
	if err := t.write(ctx, msg); err != nil {
		return nil, err
	}

	return t.awaitReply(ctx, replies)
}

// message builds the Kafka message for an envelope.
//
// Key is the pattern. That is the whole ordering story: kafka.Hash maps a key to
// a partition, a partition is ordered, so all messages for one subject arrive in
// the order they were produced. It costs nothing and it is the guarantee people
// assume they already have.
func (t *Transport) message(topic string, env *microservice.Envelope) (kafka.Message, error) {
	if env.Pattern == "" {
		return kafka.Message{}, errors.New("kafkamq: envelope has no pattern")
	}
	body, err := env.Encode()
	if err != nil {
		return kafka.Message{}, fmt.Errorf("kafkamq: encode envelope: %w", err)
	}

	headers := []kafka.Header{
		{Key: headerPattern, Value: []byte(env.Pattern)},
		{Key: headerID, Value: []byte(env.ID)},
	}
	if env.ReplyTo != "" {
		headers = append(headers, kafka.Header{Key: headerReplyTo, Value: []byte(env.ReplyTo)})
	}

	return kafka.Message{
		Topic:   topic,
		Key:     []byte(env.Pattern),
		Value:   body,
		Headers: headers,
		Time:    time.Now().UTC(),
	}, nil
}

// write produces one message, mapping the transport-level failures onto the
// microservice sentinels.
func (t *Transport) write(ctx context.Context, msg kafka.Message) error {
	ctx, cancel := t.linkClose(ctx)
	defer cancel()

	if err := t.writer.WriteMessages(ctx, msg); err != nil {
		if t.isClosed() {
			return microservice.ErrClosed
		}
		return fmt.Errorf("kafkamq: produce to %s: %w", msg.Topic, mapTimeout(err))
	}
	return nil
}

// registerPending reserves a correlation slot and returns the release the caller
// must defer. The channel is buffered so the reply consumer's send cannot block
// even if the requester has already timed out.
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

// awaitReply blocks for the correlated reply, the deadline, or Close.
func (t *Transport) awaitReply(ctx context.Context, replies <-chan *microservice.Envelope) (*microservice.Envelope, error) {
	select {
	case reply := <-replies:
		return reply, nil
	case <-t.closed:
		return nil, microservice.ErrClosed
	case <-ctx.Done():
		return nil, mapTimeout(ctx.Err())
	}
}

// ensureReplyConsumer starts the reply-topic consumer exactly once.
//
// The group id is unique to this process. A shared group would load-balance
// replies across client instances, so a reply would routinely be delivered to a
// process that never made the request and the real requester would time out. The
// cost of a unique group is that every client reads every reply on the topic and
// discards the ones it is not waiting for.
func (t *Transport) ensureReplyConsumer() error {
	t.replyOnce.Do(func() {
		if t.opts.CreateTopics {
			ctx, cancel := context.WithTimeout(t.replyCtx, t.opts.DialTimeout)
			defer cancel()
			if err := t.createTopics(ctx, t.opts.ReplyTopic); err != nil {
				t.replyErr = err
				return
			}
		}

		group := "nika-reply-" + microservice.NewID()
		cfg := t.readerConfig(t.opts.ReplyTopic, group)
		// A brand-new group with no committed offset must start at the end: an
		// ephemeral client has no interest in replies to requests made before it
		// existed, and FirstOffset here would replay the whole reply topic on
		// every process start.
		cfg.StartOffset = kafka.LastOffset

		reader := kafka.NewReader(cfg)

		t.replyMu.Lock()
		t.replyReader = reader
		t.replyMu.Unlock()

		t.replyWG.Add(1)
		go t.consumeReplies(reader)

		t.log.Debug("kafkamq reply consumer started",
			slog.String("topic", t.opts.ReplyTopic),
			slog.String("group", group))
	})
	return t.replyErr
}

// consumeReplies demultiplexes the reply topic into the pending map until the
// transport closes.
func (t *Transport) consumeReplies(reader *kafka.Reader) {
	defer t.replyWG.Done()

	for {
		msg, err := reader.FetchMessage(t.replyCtx)
		if err != nil {
			if t.isClosed() || t.replyCtx.Err() != nil {
				return
			}
			// A transient fetch error must not become a hot loop; kafka-go will
			// reconnect on the next attempt.
			t.log.Warn("kafkamq: fetching a reply failed", slog.Any("error", err))
			select {
			case <-t.closed:
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}

		t.deliverReply(msg)

		// Commit so a restart of this process does not replay replies to requests
		// that no longer exist. The group is unique to this process, so this is
		// bookkeeping rather than a delivery guarantee.
		if err := reader.CommitMessages(t.replyCtx, msg); err != nil && !t.isClosed() {
			t.log.Warn("kafkamq: committing a reply offset failed", slog.Any("error", err))
		}
	}
}

// deliverReply routes one reply message to the Request waiting for it.
func (t *Transport) deliverReply(msg kafka.Message) {
	env, err := microservice.DecodeEnvelope(msg.Value)
	if err != nil {
		// Skip, never die. The waiting Request times out, which is strictly better
		// than the reply consumer dying and every request timing out.
		t.log.Warn("kafkamq: discarding malformed reply",
			slog.String("topic", msg.Topic),
			slog.Int("partition", msg.Partition),
			slog.Int64("offset", msg.Offset),
			slog.Any("error", err))
		return
	}

	t.pendingMu.Lock()
	waiter, found := t.pending[env.ID]
	// Deleting here as well means this send can never be the second one on the
	// channel, so the buffer of 1 is always free.
	delete(t.pending, env.ID)
	t.pendingMu.Unlock()

	if !found {
		// Expected: with a unique group per client, every client sees every reply
		// on the topic and only one of them is waiting for any given id.
		return
	}
	waiter <- env
}
