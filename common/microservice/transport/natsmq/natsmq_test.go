package natsmq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nika-framework/nika/common/microservice"
)

// These tests never touch a NATS server. Options.LazyConnect is what makes that
// possible: the transport is constructible and its whole lifecycle is exercisable
// without a broker. Everything that needs a real server lives in integration_test.go
// behind the nats_integration build tag.

const testTimeout = 2 * time.Second

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestTransport(t *testing.T, mutate func(*Options)) *Transport {
	t.Helper()

	opts := Options{URL: "nats://127.0.0.1:14222", LazyConnect: true, Logger: testLogger()}
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

// ------------------------------------------------------------------ subject plan

// TestSubjectPlan is the most important test in this package. The catch-all decision
// is where a wrong answer produces silent double delivery — every literal message
// handled twice — and the mapping between character-level patterns and NATS's
// token-level wildcards is where a wrong answer produces a subscription that never
// matches anything.
func TestSubjectPlan(t *testing.T) {
	cases := []struct {
		name         string
		patterns     []string
		wantSubjects []string
		wantCatchAll bool
		wantErr      string
	}{
		{
			name:         "literals map one to one",
			patterns:     []string{"user_created", "users"},
			wantSubjects: []string{"user_created", "users"},
		},
		{
			name:         "duplicate literals collapse",
			patterns:     []string{"a", "a", "b"},
			wantSubjects: []string{"a", "b"},
		},
		{
			name:         "a star pattern forces the catch-all",
			patterns:     []string{"user_*"},
			wantCatchAll: true,
		},
		{
			name:         "a question-mark pattern forces the catch-all",
			patterns:     []string{"user_?"},
			wantCatchAll: true,
		},
		{
			// This is the double-delivery guard: `prefix.>` already matches every
			// literal subject, so subscribing to the literals as well would run
			// their handlers twice for every message.
			name:         "the catch-all replaces the literal subjects entirely",
			patterns:     []string{"user_created", "users", "user_*"},
			wantSubjects: nil,
			wantCatchAll: true,
		},
		{
			name:     "a dot is rejected because NATS reads it as a token separator",
			patterns: []string{"user.created"},
			wantErr:  `contains "."`,
		},
		{
			name:     "a literal NATS catch-all is rejected",
			patterns: []string{"user>"},
			wantErr:  `contains ">"`,
		},
		{
			name:     "a space is rejected because it terminates a subject",
			patterns: []string{"user created"},
			wantErr:  `contains " "`,
		},
		{
			name:     "a tab is rejected",
			patterns: []string{"user\tcreated"},
			wantErr:  `contains "\t"`,
		},
		{
			name:     "an empty pattern is rejected",
			patterns: []string{""},
			wantErr:  "cannot be empty",
		},
		{
			name:     "no patterns is rejected",
			patterns: nil,
			wantErr:  "no patterns",
		},
		{
			name:     "a dot inside a wildcard pattern is rejected too",
			patterns: []string{"user.*"},
			wantErr:  `contains "."`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subjects, catchAll, err := subjectPlan(tc.patterns)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if catchAll != tc.wantCatchAll {
				t.Errorf("catchAll = %v want %v", catchAll, tc.wantCatchAll)
			}
			if !reflect.DeepEqual(subjects, tc.wantSubjects) {
				t.Errorf("subjects = %v want %v", subjects, tc.wantSubjects)
			}
			if catchAll && len(subjects) != 0 {
				t.Errorf("the catch-all plan also returned %v, which would double-deliver every literal", subjects)
			}
		})
	}
}

// TestSubjectPlanNeverProducesOverlappingSubscriptions restates the invariant
// independently of the table: no plan may contain both `prefix.>` and a subject it
// covers.
func TestSubjectPlanNeverProducesOverlappingSubscriptions(t *testing.T) {
	inputs := [][]string{
		{"a"},
		{"a", "b"},
		{"a_*"},
		{"a", "a_*"},
		{"a", "b", "c_?", "d_*"},
	}

	for _, patterns := range inputs {
		subjects, catchAll, err := subjectPlan(patterns)
		if err != nil {
			t.Fatalf("subjectPlan(%v): %v", patterns, err)
		}
		if catchAll && len(subjects) > 0 {
			t.Errorf("subjectPlan(%v) returned both the catch-all and %v", patterns, subjects)
		}
		if !catchAll && len(subjects) == 0 {
			t.Errorf("subjectPlan(%v) subscribes to nothing", patterns)
		}
	}
}

func TestSubjectFor(t *testing.T) {
	got, err := subjectFor("nika", "user_created")
	if err != nil {
		t.Fatalf("subjectFor: %v", err)
	}
	if got != "nika.user_created" {
		t.Fatalf("subjectFor = %q", got)
	}

	if _, err := subjectFor("nika", "user_*"); err == nil {
		t.Error("publishing to a wildcard must be rejected")
	}
	if _, err := subjectFor("nika", "user.created"); err == nil {
		t.Error("a dotted pattern must be rejected")
	}
}

func TestCatchAllSubject(t *testing.T) {
	if got := catchAllSubject("nika"); got != "nika.>" {
		t.Fatalf("catchAllSubject = %q want %q", got, "nika.>")
	}
}

func TestValidatePrefix(t *testing.T) {
	cases := []struct {
		prefix string
		ok     bool
	}{
		{"nika", true},
		{"nika.svc", true},
		{"", false},
		{"nika.>", false},
		{"nika.*", false},
		{"ni ka", false},
		{".nika", false},
		{"nika.", false},
		{"nika..svc", false},
	}

	for _, tc := range cases {
		err := validatePrefix(tc.prefix)
		if tc.ok && err != nil {
			t.Errorf("validatePrefix(%q) = %v, want nil", tc.prefix, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("validatePrefix(%q) = nil, want an error", tc.prefix)
		}
	}
}

// ------------------------------------------------------------------ queue group

// TestResolveQueueGroup covers the knob that decides whether the service is
// load-balanced or a broadcast.
func TestResolveQueueGroup(t *testing.T) {
	cases := []struct {
		name        string
		queueGroup  string
		serviceName string
		want        string
	}{
		{"explicit group wins", "workers", "users", "workers"},
		{"a named service defaults to load balancing", "", "users", "users"},
		{"an unnamed service broadcasts", "", "", ""},
		{"the sentinel forces broadcast", NoQueueGroup, "users", ""},
		{"the sentinel with no name", NoQueueGroup, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveQueueGroup(tc.queueGroup, tc.serviceName); got != tc.want {
				t.Fatalf("resolveQueueGroup(%q, %q) = %q want %q", tc.queueGroup, tc.serviceName, got, tc.want)
			}
		})
	}
}

func TestQueueGroupAccessorReflectsTheDefaulting(t *testing.T) {
	named := newTestTransport(t, func(o *Options) { o.Name = "users" })
	if named.QueueGroup() != "users" {
		t.Errorf("QueueGroup() = %q want %q", named.QueueGroup(), "users")
	}

	broadcast := newTestTransport(t, func(o *Options) { o.Name = "users"; o.QueueGroup = NoQueueGroup })
	if broadcast.QueueGroup() != "" {
		t.Errorf("QueueGroup() = %q want broadcast", broadcast.QueueGroup())
	}
}

// -------------------------------------------------------- options and lifecycle

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{"url and conn together", Options{URL: "nats://x:4222", Conn: &nats.Conn{}}, "not both"},
		{"negative concurrency", Options{Concurrency: -1, LazyConnect: true}, "cannot be negative"},
		{"illegal prefix", Options{Prefix: "nika.>", LazyConnect: true}, "reserves"},
		{"invalid nkey seed", Options{NKeySeed: "not-a-seed", LazyConnect: true}, "invalid Options.NKeySeed"},
		{"lazy connect needs no server", Options{URL: "nats://127.0.0.1:14222", LazyConnect: true}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.opts.Logger == nil {
				tc.opts.Logger = testLogger()
			}
			tr, err := New(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				_ = tr.Close()
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	tr := newTestTransport(t, nil)

	if tr.prefix != DefaultPrefix {
		t.Errorf("prefix = %q want %q", tr.prefix, DefaultPrefix)
	}
	if tr.concurrency != defaultConcurrency {
		t.Errorf("concurrency = %d want %d", tr.concurrency, defaultConcurrency)
	}
	if tr.replyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("replyTimeout = %s", tr.replyTimeout)
	}
	if tr.drainTimeout != defaultDrainTimeout {
		t.Errorf("drainTimeout = %s", tr.drainTimeout)
	}
}

func TestNewDefaultsTheURL(t *testing.T) {
	tr, err := New(Options{LazyConnect: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if tr.url != nats.DefaultURL {
		t.Fatalf("url = %q want %q", tr.url, nats.DefaultURL)
	}
}

// TestNewConnectsEagerlyByDefault: an unreachable server is a startup problem, and
// the default is to surface it as one.
func TestNewConnectsEagerlyByDefault(t *testing.T) {
	_, err := New(Options{URL: "nats://127.0.0.1:14222", Logger: testLogger()})
	if err == nil {
		t.Fatal("expected an eager connect to a closed port to fail")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error %q does not identify the connect failure", err)
	}
}

func TestName(t *testing.T) {
	if got := newTestTransport(t, nil).Name(); got != microservice.TransportNATS {
		t.Fatalf("Name() = %q want %q", got, microservice.TransportNATS)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	tr, err := New(Options{URL: "nats://127.0.0.1:14222", LazyConnect: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr, err := New(Options{URL: "nats://127.0.0.1:14222", LazyConnect: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	env, err := microservice.NewEnvelope("user_created", map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Request(ctx, env, time.Second); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	if err := tr.Publish(ctx, env); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Publish after Close = %v, want ErrClosed", err)
	}
	if err := tr.Listen(ctx, []string{"user_created"}, dummyDispatch); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Listen after Close = %v, want ErrClosed", err)
	}
	if err := tr.Ping(ctx); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Ping after Close = %v, want ErrClosed", err)
	}
}

func dummyDispatch(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{ID: env.ID, Pattern: env.Pattern, Status: 200}, nil
}

func TestListenRejectsNilDispatcher(t *testing.T) {
	tr := newTestTransport(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := tr.Listen(ctx, []string{"a"}, nil); err == nil {
		t.Fatal("expected an error for a nil dispatcher")
	}
}

// TestListenValidatesPatternsBeforeConnecting: a bad pattern must be reported as a
// configuration error, not hidden behind a connection failure.
func TestListenValidatesPatternsBeforeConnecting(t *testing.T) {
	tr := newTestTransport(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := tr.Listen(ctx, []string{"user.created"}, dummyDispatch)
	if err == nil {
		t.Fatal("expected a pattern error")
	}
	if !strings.Contains(err.Error(), `contains "."`) {
		t.Fatalf("error %q is not the pattern error", err)
	}
}

func TestPublishAndRequestRejectNilEnvelopes(t *testing.T) {
	tr := newTestTransport(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := tr.Publish(ctx, nil); err == nil {
		t.Error("a nil envelope must be rejected")
	}
	if _, err := tr.Request(ctx, nil, time.Second); err == nil {
		t.Error("a nil envelope must be rejected")
	}
}

// TestPublishReportsTheConnectFailure: with LazyConnect the dial happens on first
// use, and its failure must reach the caller rather than being swallowed.
func TestPublishReportsTheConnectFailure(t *testing.T) {
	tr := newTestTransport(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	env, err := microservice.NewEnvelope("user_created", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(ctx, env); err == nil {
		t.Fatal("expected a connect failure")
	} else if !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error %q does not identify the connect failure", err)
	}
}

// ------------------------------------------------------------- error mapping

func TestMapNATSError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"nats timeout", nats.ErrTimeout, microservice.ErrTimeout},
		{"context deadline", context.DeadlineExceeded, microservice.ErrTimeout},
		{"no responders becomes no handler", nats.ErrNoResponders, microservice.ErrNoHandler},
		{"connection closed", nats.ErrConnectionClosed, microservice.ErrClosed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapNATSError(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("mapNATSError(nil) = %v", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapNATSError(%v) = %v want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapNATSErrorPassesUnknownErrorsThrough(t *testing.T) {
	sentinel := errors.New("something else")
	if got := mapNATSError(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("mapNATSError rewrote an unrelated error: %v", got)
	}
}

// TestContextWithCloseIsCancelledByClose covers how Close unblocks a pending
// Request: nats.RequestWithContext only watches the context, so shutdown has to be
// folded into one.
func TestContextWithCloseIsCancelledByClose(t *testing.T) {
	tr, err := New(Options{URL: "nats://127.0.0.1:14222", LazyConnect: true, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := tr.contextWithClose(context.Background())
	defer stop()

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(testTimeout):
		t.Fatal("Close did not cancel the derived context")
	}
}

func TestContextWithCloseStopsItsGoroutine(t *testing.T) {
	tr := newTestTransport(t, nil)

	// Calling stop must release the watcher goroutine even though the transport is
	// still open; otherwise every Request would leak one.
	for i := 0; i < 100; i++ {
		_, stop := tr.contextWithClose(context.Background())
		stop()
	}
}

func TestErrorReplyCarriesTheRequestID(t *testing.T) {
	env := &microservice.Envelope{ID: "abc", Pattern: "user_created"}
	reply := errorReply(env, 500, "DISPATCH_ERROR", "boom")

	if reply.ID != env.ID {
		t.Errorf("reply id = %q want %q", reply.ID, env.ID)
	}
	if reply.Pattern != env.Pattern {
		t.Errorf("reply pattern = %q want %q", reply.Pattern, env.Pattern)
	}
	if reply.Error == nil || reply.Error.Code != 500 {
		t.Errorf("reply error = %+v", reply.Error)
	}
}
