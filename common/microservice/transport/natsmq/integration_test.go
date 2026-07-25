//go:build nats_integration

// These tests need a real NATS server and are excluded from a default
// `go test ./...` by the nats_integration build tag. Run them with:
//
//	NATSMQ_TEST_URL=nats://localhost:4222 go test -tags nats_integration -race ./common/microservice/transport/natsmq/
package natsmq

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

const integrationEnvVar = "NATSMQ_TEST_URL"

func integrationURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv(integrationEnvVar)
	if url == "" {
		t.Skipf("set %s to run the NATS integration tests", integrationEnvVar)
	}
	return url
}

// uniquePrefix keeps concurrent test runs from seeing each other's traffic. The NATS
// subject space is flat and account-wide, so the prefix is the only isolation.
func uniquePrefix(t *testing.T) string {
	t.Helper()
	return "nikatest_" + microservice.NewID()[:12]
}

func newIntegrationTransport(t *testing.T, prefix string, mutate func(*Options)) *Transport {
	t.Helper()

	opts := Options{
		URL:    integrationURL(t),
		Prefix: prefix,
		Logger: testLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// startListener runs Listen in the background and blocks until it is provably
// receiving, by publishing a probe until one comes through.
func startListener(t *testing.T, tr *Transport, patterns []string, dispatch microservice.Dispatcher) {
	t.Helper()

	probePattern := "probe_" + microservice.NewID()[:8]
	probes := make(chan struct{}, 64)

	wrapped := func(ctx context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		if env.Pattern == probePattern {
			select {
			case probes <- struct{}{}:
			default:
			}
			return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
		}
		return dispatch(ctx, env)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Listen(ctx, append(patterns, probePattern), wrapped) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Listen did not return after cancellation")
		}
	})

	publisher := newIntegrationTransport(t, tr.prefix, nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env, err := microservice.NewEnvelope(probePattern, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := publisher.Publish(context.Background(), env); err != nil {
			t.Fatalf("probe publish: %v", err)
		}
		select {
		case <-probes:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("the subscription never became live")
}

func echoDispatch(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{
		ID:      env.ID,
		Pattern: env.Pattern,
		Status:  200,
		Data:    env.Data,
	}, nil
}

func TestIntegrationPing(t *testing.T) {
	tr := newIntegrationTransport(t, uniquePrefix(t), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tr.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestIntegrationRequestReply(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix, nil)
	startListener(t, server, []string{"user_created"}, echoDispatch)

	client := newIntegrationTransport(t, prefix, nil)
	env, err := microservice.NewEnvelope("user_created", map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatal(err)
	}

	reply, err := client.Request(context.Background(), env, 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID != env.ID {
		t.Errorf("reply id = %q want %q", reply.ID, env.ID)
	}
	if got := string(reply.Data); got != `{"name":"Ada"}` {
		t.Errorf("reply data = %s", got)
	}
}

// TestIntegrationRequestWithNoResponders proves the sentinel mapping: NATS reports
// "no responders" immediately, which is strictly better information than a timeout.
func TestIntegrationRequestWithNoResponders(t *testing.T) {
	client := newIntegrationTransport(t, uniquePrefix(t), nil)

	env, err := microservice.NewEnvelope("nobody_home", nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = client.Request(context.Background(), env, 5*time.Second)
	if !errors.Is(err, microservice.ErrNoHandler) && !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want ErrNoHandler (or ErrTimeout on a server without no_responders)", err)
	}
	if errors.Is(err, microservice.ErrNoHandler) && time.Since(start) > time.Second {
		t.Errorf("no-responders took %s; it should be immediate", time.Since(start))
	}
}

func TestIntegrationFireAndForget(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix, nil)

	received := make(chan *microservice.Envelope, 4)
	startListener(t, server, []string{"user_event"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := newIntegrationTransport(t, prefix, nil)
	env, err := microservice.NewEnvelope("user_event", map[string]int{"n": 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case got := <-received:
		if got.ReplyTo != "" {
			t.Errorf("a published envelope must carry no ReplyTo, got %q", got.ReplyTo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event never arrived")
	}
}

// TestIntegrationWildcardUsesTheCatchAll covers the character-level wildcard path:
// `user_*` cannot be a NATS subject, so the process subscribes to `prefix.>` and the
// core Router filters locally.
func TestIntegrationWildcardUsesTheCatchAll(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix, nil)

	received := make(chan string, 8)
	startListener(t, server, []string{"user_*"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env.Pattern
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := newIntegrationTransport(t, prefix, nil)
	for _, pattern := range []string{"user_42", "user_created"} {
		env, err := microservice.NewEnvelope(pattern, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Publish(context.Background(), env); err != nil {
			t.Fatalf("Publish %q: %v", pattern, err)
		}
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case pattern := <-received:
			seen[pattern] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only saw %v", seen)
		}
	}
	if !seen["user_42"] || !seen["user_created"] {
		t.Fatalf("the catch-all missed a subject: %v", seen)
	}
}

// TestIntegrationNoDoubleDelivery is the end-to-end proof of the subjectPlan
// decision. `prefix.>` already matches every literal subject, so also subscribing to
// the literals would make NATS deliver each literal message twice.
func TestIntegrationNoDoubleDelivery(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix, nil)

	var count int64
	startListener(t, server, []string{"user_created", "users", "user_*"},
		func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
			if env.Pattern == "user_created" {
				atomic.AddInt64(&count, 1)
			}
			return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
		})

	client := newIntegrationTransport(t, prefix, nil)
	env, err := microservice.NewEnvelope("user_created", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	time.Sleep(750 * time.Millisecond)
	if got := atomic.LoadInt64(&count); got != 1 {
		t.Fatalf("the message was dispatched %d times, want exactly 1", got)
	}
}

// TestIntegrationQueueGroupLoadBalances asserts the knob documented on
// Options.QueueGroup: with a queue group, N replicas share the work; without one,
// every replica handles every message.
func TestIntegrationQueueGroupLoadBalances(t *testing.T) {
	prefix := uniquePrefix(t)

	var total int64
	count := func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		atomic.AddInt64(&total, 1)
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	for i := 0; i < 2; i++ {
		replica := newIntegrationTransport(t, prefix, func(o *Options) { o.QueueGroup = "workers" })
		startListener(t, replica, []string{"work_item"}, count)
	}

	client := newIntegrationTransport(t, prefix, nil)
	const messages = 10
	for i := 0; i < messages; i++ {
		env, err := microservice.NewEnvelope("work_item", map[string]int{"i": i})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Publish(context.Background(), env); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&total) < messages && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	if got := atomic.LoadInt64(&total); got != messages {
		t.Fatalf("two queue-group replicas handled %d of %d messages; a queue group must deliver each message exactly once", got, messages)
	}
}

func TestIntegrationBroadcastWithoutAQueueGroup(t *testing.T) {
	prefix := uniquePrefix(t)

	var total int64
	count := func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		atomic.AddInt64(&total, 1)
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	}

	for i := 0; i < 2; i++ {
		replica := newIntegrationTransport(t, prefix, func(o *Options) { o.QueueGroup = NoQueueGroup })
		startListener(t, replica, []string{"broadcast_item"}, count)
	}

	client := newIntegrationTransport(t, prefix, nil)
	env, err := microservice.NewEnvelope("broadcast_item", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&total) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&total); got != 2 {
		t.Fatalf("without a queue group both replicas must handle the message; got %d", got)
	}
}
