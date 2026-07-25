//go:build redis_integration

// These tests need a real Redis server and are excluded from a default
// `go test ./...` by the redis_integration build tag. Run them with:
//
//	REDISMQ_TEST_URL=redis://localhost:6379/15 go test -tags redis_integration -race ./common/microservice/transport/redismq/
//
// Use a throwaway database: the tests publish on a random prefix, so they do not
// collide, but pub/sub is instance-wide and a noisy instance can make them flaky.
package redismq

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

const integrationEnvVar = "REDISMQ_TEST_URL"

func integrationURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv(integrationEnvVar)
	if url == "" {
		t.Skipf("set %s to run the Redis integration tests", integrationEnvVar)
	}
	return url
}

// uniquePrefix keeps concurrent test runs from seeing each other's traffic; Redis
// pub/sub has no other isolation mechanism.
func uniquePrefix(t *testing.T) string {
	t.Helper()
	return "nikatest:" + microservice.NewID()[:12]
}

func newIntegrationTransport(t *testing.T, prefix string) *Transport {
	t.Helper()

	tr, err := New(Options{
		URL:         integrationURL(t),
		Prefix:      prefix,
		Logger:      testLogger(),
		HealthCheck: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// startListener runs Listen in the background and blocks until it is provably
// receiving, by publishing a probe until one comes through. Sleeping instead would
// either be flaky or slow.
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

	publisher := newIntegrationTransport(t, tr.prefix)
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

func TestIntegrationPing(t *testing.T) {
	tr := newIntegrationTransport(t, uniquePrefix(t))
	if err := tr.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestIntegrationRequestReply(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix)
	startListener(t, server, []string{"user_created"}, echoDispatch)

	client := newIntegrationTransport(t, prefix)
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
	if n := client.pendingLen(); n != 0 {
		t.Errorf("pending map has %d entries after a successful request", n)
	}
}

func TestIntegrationRequestTimeoutLeavesNoPendingEntry(t *testing.T) {
	prefix := uniquePrefix(t)
	client := newIntegrationTransport(t, prefix)

	// Nobody is listening, so this can only time out.
	env, err := microservice.NewEnvelope("nobody_home", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Request(context.Background(), env, 300*time.Millisecond); !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want ErrTimeout", err)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("a timed-out request leaked %d correlation entries", n)
	}
}

func TestIntegrationFireAndForget(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix)

	received := make(chan *microservice.Envelope, 4)
	startListener(t, server, []string{"user_event"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := newIntegrationTransport(t, prefix)
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

func TestIntegrationWildcardSubscription(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix)

	received := make(chan string, 8)
	startListener(t, server, []string{"user_*"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env.Pattern
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := newIntegrationTransport(t, prefix)
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
		t.Fatalf("the wildcard subscription missed a subject: %v", seen)
	}
}

// TestIntegrationNoDoubleDelivery is the end-to-end proof of plan.accept: with both
// an exact pattern and a wildcard covering it registered, Redis delivers the message
// twice and the transport must hand it to the dispatcher once.
func TestIntegrationNoDoubleDelivery(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix)

	var count int64
	startListener(t, server, []string{"user_created", "user_*", "*_created"},
		func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
			if env.Pattern == "user_created" {
				atomic.AddInt64(&count, 1)
			}
			return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
		})

	client := newIntegrationTransport(t, prefix)
	env, err := microservice.NewEnvelope("user_created", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Give every duplicate a chance to arrive before asserting.
	time.Sleep(750 * time.Millisecond)
	if got := atomic.LoadInt64(&count); got != 1 {
		t.Fatalf("the message was dispatched %d times, want exactly 1", got)
	}
}

// TestIntegrationBracketPatternStaysLiteral proves toRedisPattern's escaping against
// a real broker: an unescaped `[1]` would make Redis match "item1" and not "item[1]".
func TestIntegrationBracketPatternStaysLiteral(t *testing.T) {
	prefix := uniquePrefix(t)
	server := newIntegrationTransport(t, prefix)

	received := make(chan string, 8)
	startListener(t, server, []string{"item[1]_*"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		received <- env.Pattern
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	client := newIntegrationTransport(t, prefix)
	for _, pattern := range []string{"item[1]_x", "item1_x"} {
		env, err := microservice.NewEnvelope(pattern, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Publish(context.Background(), env); err != nil {
			t.Fatalf("Publish %q: %v", pattern, err)
		}
	}

	select {
	case got := <-received:
		if got != "item[1]_x" {
			t.Fatalf("received %q; the bracket was interpreted as a character class", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the literal-bracket subject never arrived")
	}

	select {
	case got := <-received:
		t.Fatalf("received %q; a character class matched a subject the core matcher would reject", got)
	case <-time.After(500 * time.Millisecond):
	}
}
