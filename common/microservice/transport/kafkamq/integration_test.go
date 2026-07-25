//go:build kafka_integration

// These tests need a live Kafka cluster and are excluded from the default build,
// so `go test ./...` stays hermetic. Run them with:
//
//	KAFKA_BROKERS=localhost:9092 \
//	  go test -race -tags kafka_integration ./common/microservice/transport/kafkamq/
package kafkamq

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

func integrationBrokers(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("KAFKA_BROKERS")
	if raw == "" {
		t.Skip("KAFKA_BROKERS is not set; skipping the Kafka integration tests")
	}
	return strings.Split(raw, ",")
}

// uniqueSuffix keeps parallel runs and repeated runs from sharing a consumer
// group or a topic, which would make offsets from a previous run leak in.
func uniqueSuffix() string { return microservice.NewID()[:12] }

// serve starts a listener and returns once the group has fetched at least once,
// which is the practical signal that it is assigned partitions and will see
// anything produced from here on.
func serve(t *testing.T, tr *Transport, patterns []string, dispatch microservice.Dispatcher) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	ready := make(chan struct{})
	var once sync.Once
	wrapped := func(c context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		once.Do(func() { close(ready) })
		return dispatch(c, env)
	}

	go func() { done <- tr.Listen(ctx, patterns, wrapped) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("Listen did not return after cancellation")
		}
	})

	// Prod the group with a throwaway message until it is clearly consuming.
	warm, cancelWarm := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelWarm()
	for {
		env, _ := microservice.NewEnvelope("__warmup", nil)
		if err := tr.Publish(warm, env); err != nil {
			t.Fatalf("warm-up publish: %v", err)
		}
		select {
		case <-ready:
			return
		case <-time.After(2 * time.Second):
		case <-warm.Done():
			t.Fatal("the consumer group never started consuming")
		}
	}
}

func TestIntegrationPublishAndConsume(t *testing.T) {
	brokers := integrationBrokers(t)
	suffix := uniqueSuffix()

	tr := MustNew(Options{
		Brokers:      brokers,
		Topic:        "nika-test-events-" + suffix,
		GroupID:      "nika-test-group-" + suffix,
		CreateTopics: true,
	})
	t.Cleanup(func() { _ = tr.Close() })

	got := make(chan *microservice.Envelope, 4)
	serve(t, tr, []string{"user_created", "__warmup"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		if env.Pattern != "__warmup" {
			got <- env
		}
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	env, _ := microservice.NewEnvelope("user_created", map[string]int{"id": 7})
	if err := tr.Publish(ctx, env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case received := <-got:
		if received.ID != env.ID {
			t.Fatalf("received id %q, want %q", received.ID, env.ID)
		}
		var payload map[string]int
		if err := json.Unmarshal(received.Data, &payload); err != nil || payload["id"] != 7 {
			t.Fatalf("payload = %s (%v)", received.Data, err)
		}
	case <-ctx.Done():
		t.Fatal("the message never reached the handler")
	}
}

func TestIntegrationPerSubjectOrdering(t *testing.T) {
	brokers := integrationBrokers(t)
	suffix := uniqueSuffix()

	tr := MustNew(Options{
		Brokers:      brokers,
		Topic:        "nika-test-order-" + suffix,
		GroupID:      "nika-test-order-" + suffix,
		Concurrency:  1, // required for the guarantee under test
		Partitions:   3,
		CreateTopics: true,
	})
	t.Cleanup(func() { _ = tr.Close() })

	const count = 20
	seen := make(chan int, count)
	serve(t, tr, []string{"seq_event", "__warmup"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		if env.Pattern != "seq_event" {
			return nil, nil
		}
		var payload struct{ N int }
		if err := env.Bind(&payload); err != nil {
			t.Errorf("bind: %v", err)
			return nil, nil
		}
		seen <- payload.N
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < count; i++ {
		env, _ := microservice.NewEnvelope("seq_event", map[string]int{"N": i})
		if err := tr.Publish(ctx, env); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	for want := 0; want < count; want++ {
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("received %d, want %d — the pattern key should keep one subject on one partition", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("only %d of %d messages arrived", want, count)
		}
	}
}

func TestIntegrationRequestReply(t *testing.T) {
	brokers := integrationBrokers(t)
	suffix := uniqueSuffix()

	opts := Options{
		Brokers:      brokers,
		Topic:        "nika-test-rpc-" + suffix,
		ReplyTopic:   "nika-test-rpc-replies-" + suffix,
		GroupID:      "nika-test-rpc-" + suffix,
		CreateTopics: true,
	}

	server := MustNew(opts)
	t.Cleanup(func() { _ = server.Close() })

	serve(t, server, []string{"echo_one", "__warmup"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200, Data: env.Data}, nil
	})

	clientOpts := opts
	clientOpts.GroupID = ""
	client := MustNew(clientOpts)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	env, _ := microservice.NewEnvelope("echo_one", map[string]string{"hello": "world"})
	reply, err := client.Request(ctx, env, 45*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID != env.ID || reply.Status != 200 {
		t.Fatalf("reply = %+v", reply)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("pendingLen = %d, want 0", n)
	}
}

func TestIntegrationRequestTimesOutWithoutAServer(t *testing.T) {
	brokers := integrationBrokers(t)
	suffix := uniqueSuffix()

	client := MustNew(Options{
		Brokers:      brokers,
		Topic:        "nika-test-lonely-" + suffix,
		ReplyTopic:   "nika-test-lonely-replies-" + suffix,
		CreateTopics: true,
	})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	env, _ := microservice.NewEnvelope("nobody_listens", nil)
	if _, err := client.Request(ctx, env, 5*time.Second); !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want ErrTimeout", err)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("pendingLen = %d, want 0 after a timeout", n)
	}
}

func TestIntegrationMalformedMessageDoesNotKillTheConsumer(t *testing.T) {
	brokers := integrationBrokers(t)
	suffix := uniqueSuffix()

	tr := MustNew(Options{
		Brokers:      brokers,
		Topic:        "nika-test-poison-" + suffix,
		GroupID:      "nika-test-poison-" + suffix,
		CreateTopics: true,
	})
	t.Cleanup(func() { _ = tr.Close() })

	good := make(chan struct{}, 1)
	serve(t, tr, []string{"good_event", "__warmup"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		if env.Pattern == "good_event" {
			good <- struct{}{}
		}
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Produce garbage straight through the writer so it bypasses envelope encoding.
	garbage, err := tr.message(tr.opts.Topic, &microservice.Envelope{ID: "x", Pattern: "good_event"})
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	garbage.Value = []byte("{ this is not an envelope")
	if err := tr.write(ctx, garbage); err != nil {
		t.Fatalf("producing garbage: %v", err)
	}

	env, _ := microservice.NewEnvelope("good_event", nil)
	if err := tr.Publish(ctx, env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-good:
	case <-ctx.Done():
		t.Fatal("the consumer stopped after a malformed message")
	}
}
