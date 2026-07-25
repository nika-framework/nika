package rabbitmq

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nika-framework/nika/common/microservice"
)

// testTimeout bounds every test so a regression that deadlocks fails the run
// instead of stalling it until CI's own timeout.
const testTimeout = 2 * time.Second

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// newTestTransport builds a transport that never dials. Every assertion below is
// about translation, options and lifecycle, all of which must hold with no broker
// anywhere near the test.
func newTestTransport(t *testing.T, opts Options) *Transport {
	t.Helper()
	if opts.URL == "" && opts.Conn == nil {
		opts.URL = "amqp://guest:guest@127.0.0.1:5672/"
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestToRoutingKey(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string
		wantErr string
	}{
		{name: "literal", subject: "user_created", want: "user_created"},
		{name: "digits and dashes", subject: "order-42_paid", want: "order-42_paid"},
		{name: "star rejected", subject: "user_*", wantErr: "must be literal"},
		{name: "question mark rejected", subject: "user_?", wantErr: "must be literal"},
		{name: "dot rejected", subject: "user.created", wantErr: "word separator"},
		{name: "hash rejected", subject: "user_#", wantErr: "multi-word wildcard"},
		{name: "space rejected", subject: "user created", wantErr: "whitespace"},
		{name: "tab rejected", subject: "user\tcreated", wantErr: "whitespace"},
		{name: "empty rejected", subject: "", wantErr: "cannot be empty"},
		{name: "too long rejected", subject: strings.Repeat("a", maxKeyBytes+1), wantErr: "over the 255 byte"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toRoutingKey(tc.subject)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("toRoutingKey(%q) = %q, want error containing %q", tc.subject, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("toRoutingKey(%q) error = %v, want it to contain %q", tc.subject, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("toRoutingKey(%q): %v", tc.subject, err)
			}
			if got != tc.want {
				t.Fatalf("toRoutingKey(%q) = %q, want %q", tc.subject, got, tc.want)
			}
		})
	}
}

func TestToBindingKey(t *testing.T) {
	cases := map[string]string{
		"user_created": "user_created",
		"users":        "users",
		"user_*":       "#",
		"user_?":       "#",
		"*":            "#",
	}
	for pattern, want := range cases {
		if got := toBindingKey(pattern); got != want {
			t.Fatalf("toBindingKey(%q) = %q, want %q", pattern, got, want)
		}
	}
}

func TestPlanBindings(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     bindingPlan
		wantErr  string
	}{
		{
			name:     "literals bind their own keys, sorted",
			patterns: []string{"users", "user_created"},
			want:     bindingPlan{Keys: []string{"user_created", "users"}},
		},
		{
			name:     "duplicate literals collapse",
			patterns: []string{"users", "users"},
			want:     bindingPlan{Keys: []string{"users"}},
		},
		{
			// The decision documented in routing.go: once a catch-all is needed the
			// literal bindings are dropped because "#" already covers them.
			name:     "any wildcard collapses the whole plan to the catch-all",
			patterns: []string{"user_created", "user_*", "orders"},
			want:     bindingPlan{Keys: []string{"#"}, CatchAll: true},
		},
		{
			name:     "wildcard only",
			patterns: []string{"*"},
			want:     bindingPlan{Keys: []string{"#"}, CatchAll: true},
		},
		{name: "no patterns", patterns: nil, wantErr: "no patterns"},
		{name: "dot rejected", patterns: []string{"user.created"}, wantErr: "word separator"},
		{name: "space rejected", patterns: []string{"user created"}, wantErr: "whitespace"},
		{name: "hash rejected", patterns: []string{"#"}, wantErr: "multi-word wildcard"},
		{name: "empty rejected", patterns: []string{""}, wantErr: "cannot be empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planBindings(tc.patterns)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("planBindings(%v) = %+v, want error containing %q", tc.patterns, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("planBindings(%v) error = %v, want it to contain %q", tc.patterns, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("planBindings(%v): %v", tc.patterns, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("planBindings(%v) = %+v, want %+v", tc.patterns, got, tc.want)
			}
		})
	}
}

func TestNewRequiresURLOrConn(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with neither URL nor Conn should fail")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr := newTestTransport(t, Options{})

	if tr.opts.Exchange != DefaultExchange {
		t.Errorf("Exchange = %q, want %q", tr.opts.Exchange, DefaultExchange)
	}
	if tr.opts.ExchangeType != DefaultExchangeType {
		t.Errorf("ExchangeType = %q, want %q", tr.opts.ExchangeType, DefaultExchangeType)
	}
	if tr.opts.Queue != DefaultQueue {
		t.Errorf("Queue = %q, want %q", tr.opts.Queue, DefaultQueue)
	}
	if !tr.durable() {
		t.Error("Durable should default to true — an ephemeral queue loses messages during a restart")
	}
	if tr.opts.AutoDelete {
		t.Error("AutoDelete should default to false")
	}
	if tr.opts.AutoAck {
		t.Error("AutoAck should default to false so a crash redelivers instead of losing")
	}
	if tr.opts.Requeue {
		t.Error("Requeue should default to false to avoid a poison-message hot loop")
	}
	if tr.opts.DisableConfirms {
		t.Error("publisher confirms should default to on")
	}
	if tr.opts.Prefetch != DefaultPrefetch {
		t.Errorf("Prefetch = %d, want %d", tr.opts.Prefetch, DefaultPrefetch)
	}
	if tr.opts.Concurrency != DefaultPrefetch {
		t.Errorf("Concurrency = %d, want it to follow Prefetch (%d)", tr.opts.Concurrency, DefaultPrefetch)
	}
	if tr.opts.ReplyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("ReplyTimeout = %v, want %v", tr.opts.ReplyTimeout, microservice.DefaultRequestTimeout)
	}
	if tr.opts.Heartbeat != DefaultHeartbeat {
		t.Errorf("Heartbeat = %v, want %v", tr.opts.Heartbeat, DefaultHeartbeat)
	}
	if tr.opts.Logger == nil {
		t.Error("Logger should default to slog.Default()")
	}
	if !tr.ownsConn {
		t.Error("a transport that dialled its own connection should own it")
	}
}

func TestNewKeepsExplicitOptions(t *testing.T) {
	tr := newTestTransport(t, Options{
		Exchange:     "events",
		ExchangeType: "direct",
		Queue:        "billing",
		Durable:      Bool(false),
		AutoDelete:   true,
		Prefetch:     4,
		Concurrency:  9,
		AutoAck:      true,
		Requeue:      true,
		ReplyTimeout: 3 * time.Second,
		Heartbeat:    time.Minute,
	})

	if tr.opts.Exchange != "events" || tr.opts.ExchangeType != "direct" || tr.opts.Queue != "billing" {
		t.Errorf("explicit topology was overwritten: %+v", tr.opts)
	}
	if tr.durable() {
		t.Error("Durable(false) should be honoured")
	}
	if tr.opts.Prefetch != 4 || tr.opts.Concurrency != 9 {
		t.Errorf("Prefetch/Concurrency = %d/%d, want 4/9", tr.opts.Prefetch, tr.opts.Concurrency)
	}
	if !tr.opts.AutoAck || !tr.opts.Requeue {
		t.Error("AutoAck/Requeue should be honoured")
	}
	if tr.opts.ReplyTimeout != 3*time.Second || tr.opts.Heartbeat != time.Minute {
		t.Errorf("timings were overwritten: %+v", tr.opts)
	}
}

func TestBorrowedConnectionIsNotOwned(t *testing.T) {
	// A zero *amqp.Connection is enough: nothing here dials, and the only thing
	// under test is who owns the lifecycle.
	tr := newTestTransport(t, Options{Conn: &amqp.Connection{}})
	if tr.ownsConn {
		t.Fatal("a caller-supplied connection must not be owned by the transport")
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
	if got := tr.Name(); got != microservice.TransportRabbitMQ {
		t.Fatalf("Name() = %q, want %q", got, microservice.TransportRabbitMQ)
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

func TestCloseDoesNotCloseABorrowedConnection(t *testing.T) {
	// amqp.Connection.Close on a zero value would panic or error; reaching it at
	// all is the failure this test is about.
	tr := newTestTransport(t, Options{Conn: &amqp.Connection{}})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close should leave a borrowed connection alone, got %v", err)
	}
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr := newTestTransport(t, Options{})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	env := &microservice.Envelope{ID: microservice.NewID(), Pattern: "user_created"}
	ctx := testContext(t)

	if err := tr.Publish(ctx, env); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if _, err := tr.Request(ctx, env, time.Second); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	if err := tr.Listen(ctx, []string{"user_created"}, okDispatcher); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Listen after Close = %v, want ErrClosed", err)
	}
	if _, err := tr.session(); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("session after Close = %v, want ErrClosed", err)
	}
}

func TestNilEnvelopeIsRejected(t *testing.T) {
	tr := newTestTransport(t, Options{})
	ctx := testContext(t)

	if err := tr.Publish(ctx, nil); err == nil {
		t.Error("Publish(nil) should fail")
	}
	if _, err := tr.Request(ctx, nil, time.Second); err == nil {
		t.Error("Request(nil) should fail")
	}
}

func TestListenRejectsNilDispatcherAndBadPatterns(t *testing.T) {
	tr := newTestTransport(t, Options{})
	ctx := testContext(t)

	if err := tr.Listen(ctx, []string{"user_created"}, nil); err == nil {
		t.Error("Listen without a dispatcher should fail")
	}
	// The pattern is rejected before any dial, so this must fail fast rather than
	// time out against a broker that is not there.
	err := tr.Listen(ctx, []string{"user.created"}, okDispatcher)
	if err == nil || !strings.Contains(err.Error(), "word separator") {
		t.Errorf("Listen with a dotted pattern = %v, want a translation error", err)
	}
}

func TestPublishRejectsWildcardSubjectBeforeDialing(t *testing.T) {
	tr := newTestTransport(t, Options{})
	ctx := testContext(t)

	err := tr.Publish(ctx, &microservice.Envelope{ID: "1", Pattern: "user_*"})
	if err == nil || !strings.Contains(err.Error(), "must be literal") {
		t.Fatalf("Publish to a wildcard subject = %v, want a translation error", err)
	}
}

func TestPendingIsCleanedUpOnTimeout(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	if got := tr.pendingLen(); got != 1 {
		t.Fatalf("pendingLen after register = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	reply, err := tr.awaitReply(ctx, id, replies, make(chan struct{}))
	release()

	if !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("awaitReply = (%v, %v), want ErrTimeout", reply, err)
	}
	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen after timeout = %d, want 0 — the correlation map is leaking", got)
	}
}

func TestPendingIsCleanedUpOnClose(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)

	done := make(chan error, 1)
	go func() {
		defer release()
		_, err := tr.awaitReply(context.Background(), id, replies, make(chan struct{}))
		done <- err
	}()

	// Close must unblock the waiter rather than let it sit for its full timeout.
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

func TestPendingIsCleanedUpWhenTheSessionDies(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	dead := make(chan struct{})
	close(dead)

	_, err := tr.awaitReply(context.Background(), id, replies, dead)
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("awaitReply on a dead session = %v, want a connection-lost error", err)
	}
	release()
	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen = %d, want 0", got)
	}
}

func TestDeliverReplyRoutesByCorrelationID(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	body, err := (&microservice.Envelope{ID: id, Pattern: "user_created", Status: 200}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	tr.deliverReply(amqp.Delivery{CorrelationId: id, Body: body})

	select {
	case reply := <-replies:
		if reply.ID != id || reply.Status != 200 {
			t.Fatalf("reply = %+v, want id %q and status 200", reply, id)
		}
	default:
		t.Fatal("the reply was not routed to its waiter")
	}
	if got := tr.pendingLen(); got != 0 {
		t.Fatalf("pendingLen = %d, want 0 — the consumer should drop the entry it satisfied", got)
	}
}

func TestDeliverReplyFallsBackToEnvelopeID(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	body, _ := (&microservice.Envelope{ID: id, Pattern: "user_created"}).Encode()
	tr.deliverReply(amqp.Delivery{Body: body}) // no CorrelationId property

	select {
	case reply := <-replies:
		if reply.ID != id {
			t.Fatalf("reply id = %q, want %q", reply.ID, id)
		}
	default:
		t.Fatal("a reply without a CorrelationId property should correlate on env.ID")
	}
}

func TestDeliverReplySurvivesMalformedAndUnknownReplies(t *testing.T) {
	tr := newTestTransport(t, Options{})

	id := microservice.NewID()
	replies, release := tr.registerPending(id)
	defer release()

	// Neither of these must panic, and neither must consume the live waiter.
	tr.deliverReply(amqp.Delivery{CorrelationId: id, Body: []byte("{not json")})
	tr.deliverReply(amqp.Delivery{CorrelationId: id, Body: nil})

	unknown, _ := (&microservice.Envelope{ID: "nobody-is-waiting", Pattern: "user_created"}).Encode()
	tr.deliverReply(amqp.Delivery{CorrelationId: "nobody-is-waiting", Body: unknown})

	select {
	case reply := <-replies:
		t.Fatalf("a malformed or unknown reply reached the waiter: %+v", reply)
	default:
	}
	if got := tr.pendingLen(); got != 1 {
		t.Fatalf("pendingLen = %d, want the live waiter to still be registered", got)
	}
}

func TestQueueArgsMergeDeadLetterExchange(t *testing.T) {
	caller := amqp.Table{"x-max-length": int32(10)}
	tr := newTestTransport(t, Options{QueueArgs: caller, DeadLetterExchange: "nika.dlx"})

	args := tr.queueArgs()
	if args["x-dead-letter-exchange"] != "nika.dlx" {
		t.Errorf("x-dead-letter-exchange = %v, want nika.dlx", args["x-dead-letter-exchange"])
	}
	if args["x-max-length"] != int32(10) {
		t.Errorf("caller args were dropped: %v", args)
	}
	if _, mutated := caller["x-dead-letter-exchange"]; mutated {
		t.Error("queueArgs must not mutate the caller's table")
	}

	plain := newTestTransport(t, Options{})
	if plain.queueArgs() != nil {
		t.Error("queueArgs should be nil when nothing needs declaring")
	}
}

func TestSafeURLRedactsCredentials(t *testing.T) {
	got := safeURL("amqp://user:s3cret@broker:5672/vhost")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "user:") {
		t.Fatalf("safeURL leaked credentials: %q", got)
	}
	if !strings.Contains(got, "broker:5672") {
		t.Fatalf("safeURL dropped the host: %q", got)
	}
	if safeURL("::not a url::") == "" {
		t.Fatal("safeURL should always return something loggable")
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

func TestIsClosedErr(t *testing.T) {
	if !isClosedErr(nil) {
		t.Error("nil should count as closed-without-error")
	}
	if !isClosedErr(amqp.ErrClosed) {
		t.Error("amqp.ErrClosed should count as closed")
	}
	if isClosedErr(errors.New("boom")) {
		t.Error("an unrelated error should not be swallowed")
	}
}

func TestAmqpErrNeverReturnsNil(t *testing.T) {
	if amqpErr(nil) == nil {
		t.Fatal("amqpErr(nil) must return a non-nil error so wrapping it is safe")
	}
	src := &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED"}
	if !errors.Is(amqpErr(src), src) {
		t.Fatal("amqpErr should pass a real error through")
	}
}

// okDispatcher is a dispatcher that never fails, for tests that only care that
// Listen rejected its arguments before reaching the network.
func okDispatcher(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
}
