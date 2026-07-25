//go:build rabbitmq_integration

// These tests need a live broker and are excluded from the default build, so
// `go test ./...` stays hermetic. Run them with:
//
//	RABBITMQ_URL=amqp://guest:guest@localhost:5672/ \
//	  go test -race -tags rabbitmq_integration ./common/microservice/transport/rabbitmq/
package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/microservice"
)

func integrationURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		t.Skip("RABBITMQ_URL is not set; skipping the RabbitMQ integration tests")
	}
	return url
}

// serve starts a listener and blocks until it is ready enough for a publish to be
// routable — the queue must exist and be bound before the test publishes,
// otherwise a topic exchange drops the message and the test flakes.
func serve(t *testing.T, tr *Transport, patterns []string, dispatch microservice.Dispatcher) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Listen(ctx, patterns, dispatch) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Listen did not return after cancellation")
		}
	})

	// Poll the topology instead of sleeping a fixed amount.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := tr.connection()
		if err == nil {
			if ch, err := conn.Channel(); err == nil {
				_, err := ch.QueueDeclarePassive(tr.opts.Queue, tr.durable(), tr.opts.AutoDelete, false, false, tr.queueArgs())
				_ = ch.Close()
				if err == nil {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the consumer queue never appeared")
}

func TestIntegrationRequestReply(t *testing.T) {
	url := integrationURL(t)

	server := MustNew(Options{URL: url, Queue: "nika.test.reqrep", AutoDelete: true, Durable: Bool(false)})
	t.Cleanup(func() { _ = server.Close() })

	serve(t, server, []string{"echo_one"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200, Data: env.Data}, nil
	})

	client := MustNew(Options{URL: url, Queue: "nika.test.reqrep"})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	env, err := microservice.NewEnvelope("echo_one", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	reply, err := client.Request(ctx, env, 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.ID != env.ID {
		t.Fatalf("reply id = %q, want %q", reply.ID, env.ID)
	}
	var got map[string]string
	if err := json.Unmarshal(reply.Data, &got); err != nil {
		t.Fatalf("reply payload: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("reply payload = %v", got)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("pendingLen = %d, want 0", n)
	}
}

func TestIntegrationWildcardPatternUsesCatchAll(t *testing.T) {
	url := integrationURL(t)

	tr := MustNew(Options{URL: url, Queue: "nika.test.wildcard", AutoDelete: true, Durable: Bool(false)})
	t.Cleanup(func() { _ = tr.Close() })

	seen := make(chan string, 4)
	serve(t, tr, []string{"user_*"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		seen <- env.Pattern
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, subject := range []string{"user_23", "user_created"} {
		env, _ := microservice.NewEnvelope(subject, nil)
		if err := tr.Publish(ctx, env); err != nil {
			t.Fatalf("Publish(%s): %v", subject, err)
		}
	}

	for i := 0; i < 2; i++ {
		select {
		case <-seen:
		case <-ctx.Done():
			t.Fatalf("only %d of 2 wildcard messages arrived", i)
		}
	}

	// The catch-all must not duplicate: RabbitMQ delivers one copy per queue per
	// publish regardless of how many bindings match.
	select {
	case extra := <-seen:
		t.Fatalf("received a duplicate delivery for %q", extra)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestIntegrationFireAndForget(t *testing.T) {
	url := integrationURL(t)

	tr := MustNew(Options{URL: url, Queue: "nika.test.events", AutoDelete: true, Durable: Bool(false)})
	t.Cleanup(func() { _ = tr.Close() })

	got := make(chan struct{}, 1)
	serve(t, tr, []string{"thing_happened"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		if env.ReplyTo != "" {
			t.Errorf("a published event should carry no ReplyTo, got %q", env.ReplyTo)
		}
		got <- struct{}{}
		return nil, nil // no reply is expected, and returning none must not nack
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	env, _ := microservice.NewEnvelope("thing_happened", nil)
	if err := tr.Publish(ctx, env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("the event never reached the handler")
	}
}

func TestIntegrationConcurrentRequests(t *testing.T) {
	url := integrationURL(t)

	server := MustNew(Options{URL: url, Queue: "nika.test.concurrent", AutoDelete: true, Durable: Bool(false), Prefetch: 16})
	t.Cleanup(func() { _ = server.Close() })

	serve(t, server, []string{"echo_many"}, func(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
		return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200, Data: env.Data}, nil
	})

	client := MustNew(Options{URL: url})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env, _ := microservice.NewEnvelope("echo_many", map[string]int{"n": i})
			reply, err := client.Request(ctx, env, 10*time.Second)
			if err != nil {
				t.Errorf("Request %d: %v", i, err)
				return
			}
			var got map[string]int
			if err := json.Unmarshal(reply.Data, &got); err != nil || got["n"] != i {
				t.Errorf("request %d got the wrong reply: %s (%v)", i, reply.Data, err)
			}
		}(i)
	}
	wg.Wait()

	if n := client.pendingLen(); n != 0 {
		t.Fatalf("pendingLen = %d, want 0", n)
	}
}

func TestIntegrationRequestTimesOutWithoutAConsumer(t *testing.T) {
	url := integrationURL(t)

	client := MustNew(Options{URL: url})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	env, _ := microservice.NewEnvelope("nobody_listens", nil)
	if _, err := client.Request(ctx, env, 300*time.Millisecond); !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("Request = %v, want ErrTimeout", err)
	}
	if n := client.pendingLen(); n != 0 {
		t.Fatalf("pendingLen = %d, want 0 after a timeout", n)
	}
}
