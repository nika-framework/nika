package kafkamq

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/nika-framework/nika/common/microservice"
)

// testTimeout bounds every test so a regression that deadlocks fails the run
// instead of stalling it.
const testTimeout = 2 * time.Second

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// newTestTransport builds a transport that never talks to a broker: kafka-go's
// Reader and Writer both dial lazily, so everything asserted below holds with no
// Kafka anywhere.
func newTestTransport(t *testing.T, opts Options) *Transport {
	t.Helper()
	if len(opts.Brokers) == 0 {
		opts.Brokers = []string{"127.0.0.1:9092"}
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestNewValidatesOptions(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{name: "brokers required", opts: Options{}, wantErr: "Brokers is required"},
		{name: "blank broker", opts: Options{Brokers: []string{" "}}, wantErr: "empty address"},
		{
			name:    "max below min",
			opts:    Options{Brokers: []string{"b:9092"}, MinBytes: 100, MaxBytes: 10},
			wantErr: "must not be below MinBytes",
		},
		{
			name:    "nonsense start offset",
			opts:    Options{Brokers: []string{"b:9092"}, StartOffset: 42},
			wantErr: "FirstOffset or kafka.LastOffset",
		},
		{
			name:    "negative commit interval",
			opts:    Options{Brokers: []string{"b:9092"}, CommitInterval: -time.Second},
			wantErr: "cannot be negative",
		},
		{
			name:    "partition and group are exclusive",
			opts:    Options{Brokers: []string{"b:9092"}, Partition: 3, GroupID: "g"},
			wantErr: "mutually exclusive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := New(tc.opts)
			if err == nil {
				_ = tr.Close()
				t.Fatalf("New(%+v) should have failed with %q", tc.opts, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("New error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr := newTestTransport(t, Options{})

	if tr.opts.Topic != DefaultTopic {
		t.Errorf("Topic = %q, want %q", tr.opts.Topic, DefaultTopic)
	}
	if tr.opts.MinBytes != DefaultMinBytes {
		t.Errorf("MinBytes = %d, want %d", tr.opts.MinBytes, DefaultMinBytes)
	}
	if tr.opts.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", tr.opts.MaxBytes, DefaultMaxBytes)
	}
	if tr.opts.MaxWait != DefaultMaxWait {
		t.Errorf("MaxWait = %v, want %v", tr.opts.MaxWait, DefaultMaxWait)
	}
	if tr.opts.StartOffset != kafka.LastOffset {
		t.Errorf("StartOffset = %d, want kafka.LastOffset (%d) so a new group does not replay the retention window",
			tr.opts.StartOffset, kafka.LastOffset)
	}
	if tr.opts.Concurrency != DefaultConcurrency {
		t.Errorf("Concurrency = %d, want %d for per-partition ordering", tr.opts.Concurrency, DefaultConcurrency)
	}
	if tr.opts.CommitInterval != 0 {
		t.Errorf("CommitInterval = %v, want 0 (synchronous commit, at-least-once)", tr.opts.CommitInterval)
	}
	if tr.opts.RequiredAcks != kafka.RequireAll {
		t.Errorf("RequiredAcks = %v, want kafka.RequireAll — RequireOne loses writes on a leader failover",
			tr.opts.RequiredAcks)
	}
	if tr.opts.Async {
		t.Error("Async should default to false so a produce failure is reported")
	}
	if _, isHash := tr.opts.Balancer.(*kafka.Hash); !isHash {
		t.Errorf("Balancer = %T, want *kafka.Hash so the pattern key maps to a stable partition", tr.opts.Balancer)
	}
	if tr.opts.CreateTopics {
		t.Error("CreateTopics should default to false")
	}
	if tr.opts.Partitions != DefaultPartitions || tr.opts.ReplicationFactor != DefaultReplicationFactor {
		t.Errorf("topic geometry = %d/%d, want %d/%d",
			tr.opts.Partitions, tr.opts.ReplicationFactor, DefaultPartitions, DefaultReplicationFactor)
	}
	if tr.opts.DialTimeout != DefaultDialTimeout {
		t.Errorf("DialTimeout = %v, want %v", tr.opts.DialTimeout, DefaultDialTimeout)
	}
	if tr.opts.ReplyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("ReplyTimeout = %v, want %v", tr.opts.ReplyTimeout, microservice.DefaultRequestTimeout)
	}
	if tr.opts.Logger == nil {
		t.Error("Logger should default to slog.Default()")
	}
}

func TestNewKeepsExplicitOptions(t *testing.T) {
	tr := newTestTransport(t, Options{
		Topic:          "events",
		GroupID:        "billing",
		ReplyTopic:     "events.replies",
		MinBytes:       64,
		MaxBytes:       1 << 20,
		MaxWait:        3 * time.Second,
		StartOffset:    kafka.FirstOffset,
		Concurrency:    8,
		CommitInterval: time.Second,
		RequiredAcks:   kafka.RequireOne,
		Async:          true,
		Balancer:       &kafka.RoundRobin{},
		ReplyTimeout:   time.Second,
	})

	if tr.opts.Topic != "events" || tr.opts.GroupID != "billing" || tr.opts.ReplyTopic != "events.replies" {
		t.Errorf("topics/group were overwritten: %+v", tr.opts)
	}
	if tr.opts.StartOffset != kafka.FirstOffset {
		t.Error("an explicit FirstOffset must be honoured")
	}
	if tr.opts.Concurrency != 8 || tr.opts.CommitInterval != time.Second {
		t.Errorf("concurrency/commit interval were overwritten: %+v", tr.opts)
	}
	if tr.opts.RequiredAcks != kafka.RequireOne || !tr.opts.Async {
		t.Error("an explicit ack policy must be honoured")
	}
	if _, isRR := tr.opts.Balancer.(*kafka.RoundRobin); !isRR {
		t.Errorf("Balancer = %T, want the supplied *kafka.RoundRobin", tr.opts.Balancer)
	}
}

func TestReaderConfigCarriesTheDefaults(t *testing.T) {
	tr := newTestTransport(t, Options{Topic: "events", CommitInterval: 0})

	cfg := tr.readerConfig("events", "workers")
	if cfg.Topic != "events" || cfg.GroupID != "workers" {
		t.Errorf("readerConfig = %+v, want topic events / group workers", cfg)
	}
	if cfg.StartOffset != kafka.LastOffset {
		t.Errorf("StartOffset = %d, want kafka.LastOffset", cfg.StartOffset)
	}
	if cfg.CommitInterval != 0 {
		t.Errorf("CommitInterval = %v, want 0 for synchronous commits", cfg.CommitInterval)
	}
	if cfg.MinBytes != DefaultMinBytes || cfg.MaxBytes != DefaultMaxBytes || cfg.MaxWait != DefaultMaxWait {
		t.Errorf("fetch shape = %d/%d/%v", cfg.MinBytes, cfg.MaxBytes, cfg.MaxWait)
	}
	// kafka-go rejects Partition and GroupID together, so the group config must
	// leave Partition alone.
	if cfg.Partition != 0 {
		t.Errorf("Partition = %d, want 0 when a GroupID is set", cfg.Partition)
	}

	pinned := newTestTransport(t, Options{Partition: 3})
	if got := pinned.readerConfig("nika", "").Partition; got != 3 {
		t.Errorf("Partition = %d, want 3 when there is no group", got)
	}
}

func TestWriterUsesSafeProduceSettings(t *testing.T) {
	tr := newTestTransport(t, Options{})
	if tr.writer.RequiredAcks != kafka.RequireAll {
		t.Errorf("writer RequiredAcks = %v, want RequireAll", tr.writer.RequiredAcks)
	}
	if tr.writer.Async {
		t.Error("writer should not be async by default")
	}
	if tr.writer.Topic != DefaultTopic {
		t.Errorf("writer Topic = %q, want %q", tr.writer.Topic, DefaultTopic)
	}
}

func TestMustNewPanicsOnBadOptions(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew should panic when Options are invalid")
		}
	}()
	MustNew(Options{})
}

func TestName(t *testing.T) {
	tr := newTestTransport(t, Options{})
	if got := tr.Name(); got != microservice.TransportKafka {
		t.Fatalf("Name() = %q, want %q", got, microservice.TransportKafka)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	tr := newTestTransport(t, Options{})
	for i := 0; i < 3; i++ {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
	if !tr.isClosed() {
		t.Fatal("transport should report itself closed")
	}
}

func TestCloseIsIdempotentUnderConcurrency(t *testing.T) {
	tr := newTestTransport(t, Options{})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tr.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr := newTestTransport(t, Options{GroupID: "workers", ReplyTopic: "replies"})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := testContext(t)
	env := &microservice.Envelope{ID: microservice.NewID(), Pattern: "user_created"}

	if err := tr.Publish(ctx, env); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if _, err := tr.Request(ctx, env, time.Second); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	if err := tr.Listen(ctx, []string{"user_created"}, okDispatcher); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Listen after Close = %v, want ErrClosed", err)
	}
}

func TestListenRequiresAGroupID(t *testing.T) {
	tr := newTestTransport(t, Options{})

	err := tr.Listen(testContext(t), []string{"user_created"}, okDispatcher)
	if err == nil || !strings.Contains(err.Error(), "GroupID is required") {
		t.Fatalf("Listen without a GroupID = %v, want a clear configuration error", err)
	}
	// It must fail before touching the network, not after a dial timeout.
	if strings.Contains(strings.ToLower(err.Error()), "dial") {
		t.Fatalf("Listen should reject the configuration before dialling, got %v", err)
	}
}

func TestListenRejectsNilDispatcher(t *testing.T) {
	tr := newTestTransport(t, Options{GroupID: "workers"})
	if err := tr.Listen(testContext(t), []string{"user_created"}, nil); err == nil {
		t.Fatal("Listen without a dispatcher should fail")
	}
}

func TestRequestWithoutReplyTopicIsNotSupported(t *testing.T) {
	tr := newTestTransport(t, Options{})

	env := &microservice.Envelope{ID: microservice.NewID(), Pattern: "user_created"}
	_, err := tr.Request(testContext(t), env, time.Second)
	if !errors.Is(err, microservice.ErrNotSupported) {
		t.Fatalf("Request without a ReplyTopic = %v, want ErrNotSupported", err)
	}
	if !strings.Contains(err.Error(), "ReplyTopic") {
		t.Fatalf("the error should name the missing option, got %v", err)
	}
	if tr.pendingLen() != 0 {
		t.Fatal("an unsupported Request must not register a correlation entry")
	}
}

func TestNilEnvelopeIsRejected(t *testing.T) {
	tr := newTestTransport(t, Options{ReplyTopic: "replies"})
	ctx := testContext(t)

	if err := tr.Publish(ctx, nil); err == nil {
		t.Error("Publish(nil) should fail")
	}
	if _, err := tr.Request(ctx, nil, time.Second); err == nil {
		t.Error("Request(nil) should fail")
	}
}

func TestMessageKeysOnThePattern(t *testing.T) {
	tr := newTestTransport(t, Options{})

	env, err := microservice.NewEnvelope("user_created", map[string]int{"id": 7})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	env.ReplyTo = "replies"

	msg, err := tr.message("events", env)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	if msg.Topic != "events" {
		t.Errorf("Topic = %q, want events", msg.Topic)
	}
	// The key is what buys per-subject ordering; if this changes, ordering is gone.
	if string(msg.Key) != "user_created" {
		t.Errorf("Key = %q, want the pattern", msg.Key)
	}
	if header(msg, headerPattern) != "user_created" {
		t.Errorf("%s header = %q", headerPattern, header(msg, headerPattern))
	}
	if header(msg, headerID) != env.ID {
		t.Errorf("%s header = %q, want %q", headerID, header(msg, headerID), env.ID)
	}
	if header(msg, headerReplyTo) != "replies" {
		t.Errorf("%s header = %q, want replies", headerReplyTo, header(msg, headerReplyTo))
	}

	decoded, err := microservice.DecodeEnvelope(msg.Value)
	if err != nil {
		t.Fatalf("the produced value is not a decodable envelope: %v", err)
	}
	if decoded.ID != env.ID || decoded.Pattern != env.Pattern {
		t.Errorf("round trip lost data: %+v", decoded)
	}
	if msg.Time.IsZero() {
		t.Error("Time should be set so end-to-end latency is measurable")
	}
}

func TestMessageOmitsReplyToHeaderWhenAbsent(t *testing.T) {
	tr := newTestTransport(t, Options{})
	msg, err := tr.message("events", &microservice.Envelope{ID: "1", Pattern: "user_created"})
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	if header(msg, headerReplyTo) != "" {
		t.Fatal("a fire-and-forget message must not carry a reply-to header")
	}
}

func TestMessageRejectsAPatternlessEnvelope(t *testing.T) {
	tr := newTestTransport(t, Options{})
	if _, err := tr.message("events", &microservice.Envelope{ID: "1"}); err == nil {
		t.Fatal("an envelope with no pattern has no partition key and must be rejected")
	}
}

func TestHeaderLookup(t *testing.T) {
	msg := kafka.Message{Headers: []kafka.Header{
		{Key: "a", Value: []byte("1")},
		{Key: "b", Value: []byte("2")},
	}}
	if got := header(msg, "b"); got != "2" {
		t.Errorf("header(b) = %q, want 2", got)
	}
	if got := header(msg, "missing"); got != "" {
		t.Errorf("header(missing) = %q, want empty", got)
	}
}

func TestPendingIsCleanedUpOnTimeout(t *testing.T) {
	tr := newTestTransport(t, Options{ReplyTopic: "replies"})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	if got := tr.pendingLen(); got != 1 {
		t.Fatalf("pendingLen after register = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	reply, err := tr.awaitReply(ctx, replies)
	release()

	if !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("awaitReply = (%v, %v), want ErrTimeout", reply, err)
	}
	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen after timeout = %d, want 0 — the correlation map is leaking", got)
	}
}

func TestCloseUnblocksAPendingRequest(t *testing.T) {
	tr := newTestTransport(t, Options{ReplyTopic: "replies"})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)

	done := make(chan error, 1)
	go func() {
		defer release()
		_, err := tr.awaitReply(context.Background(), replies)
		done <- err
	}()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, microservice.ErrClosed) {
			t.Fatalf("awaitReply after Close = %v, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock a pending Request")
	}

	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen after Close = %d, want 0", got)
	}
}

func TestDeliverReplyRoutesByEnvelopeID(t *testing.T) {
	tr := newTestTransport(t, Options{ReplyTopic: "replies"})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	body, err := (&microservice.Envelope{ID: id, Pattern: "user_created", Status: 200}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	tr.deliverReply(kafka.Message{Topic: "replies", Value: body})

	select {
	case reply := <-replies:
		if reply.ID != id || reply.Status != 200 {
			t.Fatalf("reply = %+v", reply)
		}
	default:
		t.Fatal("the reply was not routed to its waiter")
	}
	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen = %d, want 0", got)
	}
}

func TestDeliverReplySurvivesMalformedAndForeignReplies(t *testing.T) {
	tr := newTestTransport(t, Options{ReplyTopic: "replies"})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	// Malformed: logged and skipped, never fatal to the consumer.
	tr.deliverReply(kafka.Message{Topic: "replies", Value: []byte("{nope")})
	tr.deliverReply(kafka.Message{Topic: "replies", Value: nil})

	// Foreign: with a unique group per client, every client sees every reply.
	foreign, _ := (&microservice.Envelope{ID: "someone-else", Pattern: "user_created"}).Encode()
	tr.deliverReply(kafka.Message{Topic: "replies", Value: foreign})

	select {
	case reply := <-replies:
		t.Fatalf("a malformed or foreign reply reached the waiter: %+v", reply)
	default:
	}
	if got := tr.pendingLen(); got != 1 {
		t.Fatalf("pendingLen = %d, want the live waiter still registered", got)
	}
}

func TestLinkCloseCancelsOnTransportClose(t *testing.T) {
	tr := newTestTransport(t, Options{})

	ctx, cancel := tr.linkClose(context.Background())
	defer cancel()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(testTimeout):
		t.Fatal("linkClose did not cancel when the transport closed")
	}
}

func TestMapTimeout(t *testing.T) {
	if got := mapTimeout(context.DeadlineExceeded); !errors.Is(got, microservice.ErrTimeout) {
		t.Errorf("mapTimeout(DeadlineExceeded) = %v, want ErrTimeout", got)
	}
	if got := mapTimeout(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("mapTimeout(Canceled) = %v, want it passed through", got)
	}
	if got := mapTimeout(nil); got != nil {
		t.Errorf("mapTimeout(nil) = %v, want nil", got)
	}
}

func TestIsTopicExists(t *testing.T) {
	if !isTopicExists(kafka.TopicAlreadyExists) {
		t.Error("TopicAlreadyExists should be treated as success")
	}
	if isTopicExists(kafka.InvalidPartitionNumber) {
		t.Error("an unrelated Kafka error should not be swallowed")
	}
	if isTopicExists(errors.New("boom")) {
		t.Error("a non-Kafka error should not be swallowed")
	}
}

func okDispatcher(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
}
