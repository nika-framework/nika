// Package kafkamq implements the microservice transport over Apache Kafka using
// segmentio/kafka-go.
//
// # One topic, pattern in the envelope
//
// Every message goes to a single topic and the subject travels in the envelope
// (and in a Kafka header, for tooling). A topic per pattern is the obvious first
// design and the wrong one:
//
//   - Kafka's unit of parallelism is the partition, not the topic. A thousand
//     patterns become a thousand topics and at least a thousand partitions, each
//     with its own replicas, index files, open file handles and controller
//     metadata. Partition count is the number that actually limits a Kafka
//     cluster.
//   - Topics have no wildcard subscriptions. A regex consumer exists but it
//     matches topic *names* and rebalances the whole group whenever a topic
//     appears, which is exactly what a dynamic pattern space would do.
//   - Ordering is per partition. Splitting one logical stream across topics
//     forfeits any ordering guarantee between them.
//
// With one topic, the message Key is set to the pattern, so every message for a
// subject hashes to the same partition and is therefore delivered in order — the
// property people actually want, for free. The core Router does the pattern
// matching in-process.
//
// # Delivery semantics
//
// Producing waits for the full in-sync replica set by default (RequireAll).
// kafka-go itself defaults to RequireNone, which returns success as soon as the
// bytes are written and loses the write outright if the partition leader fails
// before replicating. That default is not repeated here.
//
// Consuming is at-least-once: FetchMessage, dispatch, then CommitMessages. A
// crash before the commit redelivers. See Options.CommitInterval for the
// at-most-once alternative and Listen for what happens when a handler fails.
//
// # Request/reply
//
// Implemented, but Kafka is the wrong tool for it: a synchronous call pays a
// produce round trip, a fetch round trip and a consumer-group join, and the reply
// topic is readable by every client that consumes it. Use NATS, RabbitMQ or gRPC
// for RPC and keep Kafka for the event log. Request returns
// microservice.ErrNotSupported unless Options.ReplyTopic is set.
package kafkamq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"

	"github.com/nika-framework/nika/common/microservice"
)

// Defaults applied to a zero Options.
const (
	// DefaultTopic carries every Nika message unless told otherwise.
	DefaultTopic = "nika"

	// DefaultMinBytes lets the broker answer a fetch as soon as it has anything,
	// which is what keeps latency low on a quiet topic.
	DefaultMinBytes = 1

	// DefaultMaxBytes bounds one fetch response. It must exceed the largest
	// envelope or the broker truncates and the consumer stalls; 8 MiB matches the
	// envelope size cap in the microservice package.
	DefaultMaxBytes = 8 << 20

	// DefaultMaxWait is how long a fetch waits for MinBytes before returning
	// empty. kafka-go's own default of 10s makes shutdown feel hung.
	DefaultMaxWait = time.Second

	// DefaultConcurrency is 1, because concurrency and per-partition ordering are
	// mutually exclusive and ordering is the reason to be on Kafka.
	DefaultConcurrency = 1

	// DefaultPartitions and DefaultReplicationFactor are used only by
	// CreateTopics, for local development.
	DefaultPartitions        = 1
	DefaultReplicationFactor = 1

	// DefaultDialTimeout bounds the reader's connection attempts.
	DefaultDialTimeout = 10 * time.Second
)

// Options configures a Kafka transport.
type Options struct {
	// Brokers is the bootstrap broker list. Required.
	Brokers []string

	// Topic carries every message. Defaults to DefaultTopic.
	Topic string

	// GroupID is the consumer group and is REQUIRED by Listen. Without a group,
	// every replica of a service reads every partition, so every replica handles
	// every message: the service is not load-balanced, it is fanned out, and the
	// duplicate side effects usually go unnoticed until production. Listen returns
	// a clear error rather than starting in that mode.
	GroupID string

	// ReplyTopic is where replies to Request are produced and consumed. Request
	// returns microservice.ErrNotSupported while it is empty.
	//
	// Every client consuming this topic sees every reply on it — correlation is
	// by envelope id, not by broker-side addressing. Envelope ids are
	// cryptographically random so they cannot be guessed, but the payloads are
	// still visible, so a reply topic must not be shared across a trust boundary.
	ReplyTopic string

	// Partition pins a reader to one partition. It is mutually exclusive with
	// GroupID and is only useful for a single-consumer tool or a test.
	Partition int

	// MinBytes, MaxBytes and MaxWait shape one fetch. See the Default constants.
	MinBytes int
	MaxBytes int
	MaxWait  time.Duration

	// StartOffset is where a consumer group begins when it has no committed
	// offset: kafka.FirstOffset or kafka.LastOffset. Defaults to
	// kafka.LastOffset.
	//
	// kafka-go defaults to FirstOffset, which replays the entire retention
	// window — days or weeks of events — the first time a new group starts. That
	// is occasionally what you want and never what you expect, so the default
	// here is LastOffset. Set FirstOffset deliberately when you mean to replay.
	StartOffset int64

	// Concurrency bounds handlers running at once per Listen. Defaults to
	// DefaultConcurrency (1).
	//
	// Anything above 1 gives up per-partition ordering, because two messages with
	// the same key are then handled simultaneously, and it weakens the delivery
	// guarantee: offsets are committed as each handler finishes, and Kafka tracks
	// a single watermark per partition, so committing message 7 while 5 and 6 are
	// still running marks them delivered. Keep it at 1 whenever ordering or strict
	// at-least-once matters.
	Concurrency int

	// CommitInterval selects how offsets are committed.
	//
	// 0 (the default) commits synchronously after the handler returns:
	// at-least-once. A crash redelivers the last message rather than losing it.
	//
	// Greater than 0 commits in the background on a timer. Commits then race the
	// handlers, so an offset can be committed for a message that has not been
	// handled and a crash loses it: at-most-once. It buys throughput on
	// high-volume topics where a dropped message is acceptable. It is a real
	// trade-off, not a tuning knob.
	CommitInterval time.Duration

	// Balancer chooses the partition for a produced message. Defaults to
	// kafka.Hash, which is what makes Key (the pattern) map to a stable
	// partition. Replacing it with kafka.RoundRobin or kafka.LeastBytes gives up
	// per-subject ordering.
	Balancer kafka.Balancer

	// RequiredAcks is how many replicas must acknowledge a produce. Defaults to
	// kafka.RequireAll.
	//
	// kafka.RequireOne acknowledges from the leader alone, so an unreplicated
	// write is lost when that leader fails over. kafka.RequireNone does not wait
	// at all. Neither can be selected here: RequireNone is the zero value and is
	// treated as "unset", which is the price of making the safe setting the
	// default. Use kafka-go directly if you truly need it.
	RequiredAcks kafka.RequiredAcks

	// Async makes Publish return before the broker answers. Defaults to false —
	// with Async the write error is delivered to nobody and Publish cannot fail,
	// which quietly turns RequiredAcks into a decoration.
	Async bool

	// Dialer is used by readers. When nil one is built from DialTimeout, TLS and
	// SASL.
	Dialer *kafka.Dialer

	// Transport is used by the writer. When nil one is built from TLS and SASL.
	Transport kafka.RoundTripper

	// TLS and SASL configure authentication for the generated Dialer and
	// Transport. They are ignored when Dialer/Transport are supplied.
	TLS  *tls.Config
	SASL sasl.Mechanism

	// DialTimeout bounds a connection attempt. Defaults to DefaultDialTimeout.
	DialTimeout time.Duration

	// ReplyTimeout bounds a Request when the caller passes no timeout. Defaults
	// to microservice.DefaultRequestTimeout.
	ReplyTimeout time.Duration

	// CreateTopics creates Topic and ReplyTopic if they are missing, so a
	// developer with a bare broker can run the service.
	//
	// It defaults to false on purpose. Auto-creating a topic in production gives
	// it whatever partition count and replication factor the client asked for,
	// and neither can be reduced afterwards — a topic created with one partition
	// caps that stream's throughput permanently, and one created with replication
	// factor 1 loses data when a broker dies. Topics are infrastructure.
	CreateTopics bool

	// Partitions and ReplicationFactor are used only by CreateTopics.
	Partitions        int
	ReplicationFactor int

	// Logger receives malformed-message and lifecycle events. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Transport is a Kafka transport. It is safe for concurrent use.
type Transport struct {
	opts Options
	log  *slog.Logger

	writer *kafka.Writer

	// The reply side is built on first use: a client that only publishes should
	// not join a consumer group, and a Listen-only service should not either.
	replyOnce   sync.Once
	replyErr    error
	replyCtx    context.Context
	replyCancel context.CancelFunc
	replyWG     sync.WaitGroup

	replyMu     sync.Mutex
	replyReader *kafka.Reader

	// pending correlates an in-flight Request with the channel its reply arrives
	// on. Entries are removed in a defer on every path, including timeout.
	pendingMu sync.Mutex
	pending   map[string]chan *microservice.Envelope

	closeOnce sync.Once
	closed    chan struct{}
}

// New returns a Kafka transport.
//
// No connection is made here: kafka-go's Reader and Writer dial lazily, so a
// broker that is briefly unavailable does not stop the process from starting.
func New(opts Options) (*Transport, error) {
	if len(opts.Brokers) == 0 {
		return nil, errors.New("kafkamq: Options.Brokers is required")
	}
	for _, broker := range opts.Brokers {
		if strings.TrimSpace(broker) == "" {
			return nil, errors.New("kafkamq: Options.Brokers contains an empty address")
		}
	}
	if opts.Topic == "" {
		opts.Topic = DefaultTopic
	}
	if opts.MinBytes <= 0 {
		opts.MinBytes = DefaultMinBytes
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxBytes < opts.MinBytes {
		return nil, fmt.Errorf("kafkamq: MaxBytes (%d) must not be below MinBytes (%d)", opts.MaxBytes, opts.MinBytes)
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = DefaultMaxWait
	}
	if opts.StartOffset == 0 {
		opts.StartOffset = kafka.LastOffset
	}
	if opts.StartOffset != kafka.FirstOffset && opts.StartOffset != kafka.LastOffset {
		return nil, fmt.Errorf("kafkamq: StartOffset must be kafka.FirstOffset or kafka.LastOffset, got %d", opts.StartOffset)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.CommitInterval < 0 {
		return nil, fmt.Errorf("kafkamq: CommitInterval cannot be negative")
	}
	if opts.Balancer == nil {
		opts.Balancer = &kafka.Hash{}
	}
	if opts.RequiredAcks == 0 {
		opts.RequiredAcks = kafka.RequireAll
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	if opts.ReplyTimeout <= 0 {
		opts.ReplyTimeout = microservice.DefaultRequestTimeout
	}
	if opts.Partitions <= 0 {
		opts.Partitions = DefaultPartitions
	}
	if opts.ReplicationFactor <= 0 {
		opts.ReplicationFactor = DefaultReplicationFactor
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Partition != 0 && opts.GroupID != "" {
		return nil, errors.New("kafkamq: Partition and GroupID are mutually exclusive — a group assigns partitions for you")
	}

	replyCtx, replyCancel := context.WithCancel(context.Background())

	t := &Transport{
		opts:        opts,
		log:         opts.Logger,
		replyCtx:    replyCtx,
		replyCancel: replyCancel,
		pending:     make(map[string]chan *microservice.Envelope),
		closed:      make(chan struct{}),
	}
	t.writer = t.newWriter()
	return t, nil
}

// MustNew is New, panicking on a configuration error.
func MustNew(opts Options) *Transport {
	t, err := New(opts)
	if err != nil {
		panic(err)
	}
	return t
}

// Name implements microservice.Listener and microservice.Publisher.
func (t *Transport) Name() string { return microservice.TransportKafka }

// newWriter builds the producer. The modern kafka.Writer is configured as a
// struct rather than through the deprecated kafka.NewWriter(WriterConfig), which
// silently ignores several of these fields.
func (t *Transport) newWriter() *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(t.opts.Brokers...),
		Topic:    t.opts.Topic,
		Balancer: t.opts.Balancer,
		// Acknowledgement policy and the async flag are the two settings that
		// decide whether a produce can be lost; see Options.
		RequiredAcks: t.opts.RequiredAcks,
		Async:        t.opts.Async,
		Transport:    t.transport(),
		// Allow the batch to close quickly: this transport carries commands and
		// events, not a bulk load, so latency beats batch efficiency.
		BatchTimeout: 10 * time.Millisecond,
	}
}

// transport returns the writer's RoundTripper, building one from TLS/SASL when
// the caller did not supply it.
func (t *Transport) transport() kafka.RoundTripper {
	if t.opts.Transport != nil {
		return t.opts.Transport
	}
	if t.opts.TLS == nil && t.opts.SASL == nil {
		return nil // kafka-go's DefaultTransport
	}
	return &kafka.Transport{
		DialTimeout: t.opts.DialTimeout,
		TLS:         t.opts.TLS,
		SASL:        t.opts.SASL,
	}
}

// dialer returns the reader's dialer, building one from TLS/SASL when the caller
// did not supply it.
func (t *Transport) dialer() *kafka.Dialer {
	if t.opts.Dialer != nil {
		return t.opts.Dialer
	}
	if t.opts.TLS == nil && t.opts.SASL == nil {
		return nil // kafka-go's DefaultDialer
	}
	return &kafka.Dialer{
		Timeout:       t.opts.DialTimeout,
		DualStack:     true,
		TLS:           t.opts.TLS,
		SASLMechanism: t.opts.SASL,
	}
}

// readerConfig builds a reader configuration. It is a separate, side-effect-free
// function so the defaults can be asserted in a test without a broker — the
// options that matter most here (StartOffset, CommitInterval, RequiredAcks) are
// precisely the ones whose effect is invisible until something is lost.
func (t *Transport) readerConfig(topic, groupID string) kafka.ReaderConfig {
	cfg := kafka.ReaderConfig{
		Brokers:        t.opts.Brokers,
		Topic:          topic,
		GroupID:        groupID,
		Dialer:         t.dialer(),
		MinBytes:       t.opts.MinBytes,
		MaxBytes:       t.opts.MaxBytes,
		MaxWait:        t.opts.MaxWait,
		StartOffset:    t.opts.StartOffset,
		CommitInterval: t.opts.CommitInterval,
	}
	if groupID == "" {
		// Only meaningful without a group; kafka-go rejects both being set.
		cfg.Partition = t.opts.Partition
	}
	return cfg
}

// Close shuts the producer and the reply consumer down. It is idempotent and
// unblocks every in-flight Request and Listen.
func (t *Transport) Close() error {
	var firstErr error

	t.closeOnce.Do(func() {
		// Release the waiters first so a slow broker teardown cannot hold them.
		close(t.closed)
		t.replyCancel()

		t.replyMu.Lock()
		reader := t.replyReader
		t.replyReader = nil
		t.replyMu.Unlock()

		if reader != nil {
			// Closing the reader unblocks a FetchMessage that is mid-flight, which
			// is what lets the reply consumer goroutine exit.
			if err := reader.Close(); err != nil {
				firstErr = fmt.Errorf("kafkamq: closing the reply reader: %w", err)
			}
		}

		// Bounded: a broker that will not let go of a connection must not be able
		// to hang shutdown.
		drained := make(chan struct{})
		go func() {
			t.replyWG.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.log.Warn("kafkamq: the reply consumer did not stop within 5s; abandoning it")
		}

		if err := t.writer.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("kafkamq: closing the writer: %w", err)
		}

		t.pendingMu.Lock()
		t.pending = make(map[string]chan *microservice.Envelope)
		t.pendingMu.Unlock()
	})

	return firstErr
}

func (t *Transport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// pendingLen reports the number of in-flight correlation entries, so tests can
// assert that a timed-out Request leaves nothing behind.
func (t *Transport) pendingLen() int {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return len(t.pending)
}

// linkClose derives a context that is also cancelled when the transport closes,
// so a blocking broker call cannot survive Close. The helper goroutine ends with
// the returned cancel, so it cannot leak.
func (t *Transport) linkClose(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-ctx.Done():
		case <-t.closed:
			cancel()
		}
	}()
	return ctx, cancel
}

// createTopics creates Topic and ReplyTopic when Options.CreateTopics is set. An
// already-existing topic is not an error.
func (t *Transport) createTopics(ctx context.Context, topics ...string) error {
	configs := make([]kafka.TopicConfig, 0, len(topics))
	for _, topic := range topics {
		if topic == "" {
			continue
		}
		configs = append(configs, kafka.TopicConfig{
			Topic:             topic,
			NumPartitions:     t.opts.Partitions,
			ReplicationFactor: t.opts.ReplicationFactor,
		})
	}
	if len(configs) == 0 {
		return nil
	}

	client := &kafka.Client{Addr: kafka.TCP(t.opts.Brokers...), Timeout: t.opts.DialTimeout}
	res, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{Topics: configs})
	if err != nil {
		return fmt.Errorf("kafkamq: create topics: %w", err)
	}
	for topic, topicErr := range res.Errors {
		if topicErr == nil || isTopicExists(topicErr) {
			continue
		}
		return fmt.Errorf("kafkamq: create topic %q: %w", topic, topicErr)
	}
	return nil
}

// isTopicExists reports whether err is Kafka's "topic already exists", which is
// the expected outcome of a second service starting against the same cluster.
func isTopicExists(err error) bool {
	var kerr kafka.Error
	if errors.As(err, &kerr) {
		return kerr == kafka.TopicAlreadyExists
	}
	return false
}

// mapTimeout normalises a deadline onto microservice.ErrTimeout.
func mapTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return microservice.ErrTimeout
	}
	return err
}
