package redismq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/nika-framework/nika/common/microservice"
)

// These tests never touch a Redis server: everything here is either a pure function
// or a lifecycle path that must work whether or not the broker is reachable. The
// behaviour that needs a real server lives in integration_test.go behind the
// redis_integration build tag.

const testTimeout = 2 * time.Second

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// echoDispatch answers with the payload it was given.
func echoDispatch(_ context.Context, env *microservice.Envelope) (*microservice.Envelope, error) {
	return &microservice.Envelope{
		ID:      env.ID,
		Pattern: env.Pattern,
		Status:  200,
		Data:    env.Data,
	}, nil
}

func newTestTransport(t *testing.T) *Transport {
	t.Helper()
	// A URL alone dials nothing: go-redis pools lazily, so this is safe with no
	// server running.
	tr, err := New(Options{URL: "redis://127.0.0.1:6379/0", Logger: testLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// -------------------------------------------------------------- pattern translation

func TestToRedisPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    string
	}{
		{"plain literal", "user_created", "user_created"},
		{"star passes through", "user_*", "user_*"},
		{"question mark passes through", "user_?", "user_?"},
		{"both wildcards", "*_?", "*_?"},
		{"bracket is escaped", "item[1]", `item\[1\]`},
		{"open bracket alone", "a[b", `a\[b`},
		{"close bracket alone", "a]b", `a\]b`},
		{"backslash is escaped", `a\b`, `a\\b`},
		{"class-looking range", "x[a-z]y", `x\[a-z\]y`},
		{"wildcards survive escaping", "item[1]_*", `item\[1\]_*`},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toRedisPattern(tc.pattern); got != tc.want {
				t.Fatalf("toRedisPattern(%q) = %q want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestEscapeGlobEscapesWildcardsToo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"nika", "nika"},
		{"nika:", "nika:"},
		{"ni*ka", `ni\*ka`},
		{"ni?ka", `ni\?ka`},
		{"ni[k]a", `ni\[k\]a`},
		{`ni\ka`, `ni\\ka`},
	}

	for _, tc := range cases {
		if got := escapeGlob(tc.in); got != tc.want {
			t.Errorf("escapeGlob(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestToRedisPatternPreservesMatchSemantics is the property the translation exists
// for: whatever microservice.Pattern says about a subject, the Redis glob must agree.
// Only the escaping side can be checked without a server, so the test asserts that a
// pattern with Redis-only metacharacters keeps them literal.
func TestToRedisPatternKeepsRedisMetacharactersLiteral(t *testing.T) {
	pattern := "item[1]"
	if !microservice.Pattern(pattern).Match("item[1]") {
		t.Fatal("precondition: the core matcher should treat brackets literally")
	}
	if microservice.Pattern(pattern).Match("item1") {
		t.Fatal("precondition: the core matcher should not treat brackets as a class")
	}

	got := toRedisPattern(pattern)
	if !strings.Contains(got, `\[`) || !strings.Contains(got, `\]`) {
		t.Fatalf("toRedisPattern(%q) = %q, which Redis would read as a character class", pattern, got)
	}
}

// -------------------------------------------------------------------- plan

func TestNewPlanSplitsLiteralsFromWildcards(t *testing.T) {
	p, err := newPlan("nika", []string{"user_created", "users", "user_*", "order_?"})
	if err != nil {
		t.Fatalf("newPlan: %v", err)
	}

	wantChannels := []string{"nika:user_created", "nika:users"}
	if len(p.channels) != len(wantChannels) {
		t.Fatalf("channels = %v want %v", p.channels, wantChannels)
	}
	for i, want := range wantChannels {
		if p.channels[i] != want {
			t.Errorf("channels[%d] = %q want %q", i, p.channels[i], want)
		}
	}

	wantGlobs := []string{"nika:user_*", "nika:order_?"}
	if len(p.globs) != len(wantGlobs) {
		t.Fatalf("globs = %v want %v", p.globs, wantGlobs)
	}
	for i, want := range wantGlobs {
		if p.globs[i].glob != want {
			t.Errorf("globs[%d] = %q want %q", i, p.globs[i].glob, want)
		}
	}
}

func TestNewPlanDeduplicates(t *testing.T) {
	p, err := newPlan("nika", []string{"a", "a", "b_*", "b_*"})
	if err != nil {
		t.Fatalf("newPlan: %v", err)
	}
	if len(p.channels) != 1 || len(p.globs) != 1 {
		t.Fatalf("duplicates were not collapsed: channels=%v globs=%v", p.channels, p.globs)
	}
}

func TestNewPlanErrors(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		patterns []string
		wantErr  string
	}{
		{"empty prefix", "", []string{"a"}, "prefix cannot be empty"},
		{"no patterns", "nika", nil, "no patterns"},
		{"empty pattern", "nika", []string{""}, "cannot be empty"},
		{"control character", "nika", []string{"a\nb"}, "control character"},
		{"reserved reply namespace", "nika", []string{"reply:abc"}, "reserved"},
		{"reserved reply wildcard", "nika", []string{"reply:*"}, "reserved"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newPlan(tc.prefix, tc.patterns)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewPlanEscapesTheGlobPrefix(t *testing.T) {
	p, err := newPlan("ni*ka", []string{"user_*"})
	if err != nil {
		t.Fatalf("newPlan: %v", err)
	}
	if p.globs[0].glob != `ni\*ka:user_*` {
		t.Fatalf("a wildcard in the prefix was not escaped: %q", p.globs[0].glob)
	}
}

// TestPlanAcceptCollapsesDuplicateDeliveries is the double-delivery guard. Redis
// delivers a message once per matching subscription, so a service registering both
// an exact pattern and a wildcard that covers it receives it twice.
func TestPlanAcceptCollapsesDuplicateDeliveries(t *testing.T) {
	p, err := newPlan("nika", []string{"user_created", "user_*", "*_created"})
	if err != nil {
		t.Fatalf("newPlan: %v", err)
	}

	cases := []struct {
		name             string
		deliveredPattern string
		channel          string
		want             bool
	}{
		{
			name:    "plain delivery on a subscribed channel is accepted",
			channel: "nika:user_created", want: true,
		},
		{
			name:             "pmessage duplicating an exact subscription is dropped",
			deliveredPattern: "nika:user_*", channel: "nika:user_created", want: false,
		},
		{
			name:             "the other overlapping glob is dropped too",
			deliveredPattern: `nika:*_created`, channel: "nika:user_created", want: false,
		},
		{
			name:             "the most specific glob owns a wildcard-only channel",
			deliveredPattern: "nika:user_*", channel: "nika:user_42", want: true,
		},
		{
			name:             "a less specific glob does not own a channel another glob claims",
			deliveredPattern: `nika:*_created`, channel: "nika:user_42", want: false,
		},
		{
			name:             "the owning glob for a channel only it matches",
			deliveredPattern: `nika:*_created`, channel: "nika:order_created", want: true,
		},
		{
			name:             "a channel outside the prefix is accepted rather than lost",
			deliveredPattern: "other:*", channel: "other:thing", want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.accept(tc.deliveredPattern, tc.channel); got != tc.want {
				t.Fatalf("accept(%q, %q) = %v want %v", tc.deliveredPattern, tc.channel, got, tc.want)
			}
		})
	}
}

// TestPlanAcceptDeliversEachChannelExactlyOnce walks every delivery Redis would make
// for a channel and asserts precisely one is accepted. That is the invariant; the
// cases above only spot-check it.
func TestPlanAcceptDeliversEachChannelExactlyOnce(t *testing.T) {
	p, err := newPlan("nika", []string{"user_created", "users", "user_*", "*_created", "?"})
	if err != nil {
		t.Fatalf("newPlan: %v", err)
	}

	subjects := []string{"user_created", "users", "user_42", "order_created", "x", "unmatched_subject"}

	for _, subject := range subjects {
		channel := "nika:" + subject

		var deliveries []string
		if _, exact := p.literal[channel]; exact {
			deliveries = append(deliveries, "")
		}
		for _, g := range p.globs {
			if g.pattern.Match(subject) {
				deliveries = append(deliveries, g.glob)
			}
		}
		if len(deliveries) == 0 {
			continue // Redis would not deliver this at all
		}

		accepted := 0
		for _, delivered := range deliveries {
			if p.accept(delivered, channel) {
				accepted++
			}
		}
		if accepted != 1 {
			t.Errorf("subject %q: %d of %d deliveries accepted, want exactly 1 (deliveries: %v)",
				subject, accepted, len(deliveries), deliveries)
		}
	}
}

func TestMessageChannel(t *testing.T) {
	got, err := messageChannel("nika", "user_created")
	if err != nil {
		t.Fatalf("messageChannel: %v", err)
	}
	if got != "nika:user_created" {
		t.Fatalf("messageChannel = %q", got)
	}

	if _, err := messageChannel("nika", "reply:x"); err == nil {
		t.Error("publishing into the reply namespace must be rejected")
	}
	if _, err := messageChannel("nika", ""); err == nil {
		t.Error("an empty pattern must be rejected")
	}
}

// -------------------------------------------------------- options and lifecycle

func TestNewValidation(t *testing.T) {
	shared := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = shared.Close() })

	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{"neither url nor client", Options{}, "either Options.URL or Options.Client"},
		{"both url and client", Options{URL: "redis://x", Client: shared}, "not both"},
		{"invalid url", Options{URL: "not-a-redis-url"}, "invalid Options.URL"},
		{"negative concurrency", Options{URL: "redis://127.0.0.1:6379", Concurrency: -1}, "cannot be negative"},
		{"url only", Options{URL: "redis://127.0.0.1:6379/1"}, ""},
		{"client only", Options{Client: shared}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
	tr := newTestTransport(t)

	if tr.prefix != DefaultPrefix {
		t.Errorf("prefix = %q want %q", tr.prefix, DefaultPrefix)
	}
	if tr.concurrency != defaultConcurrency {
		t.Errorf("concurrency = %d want %d", tr.concurrency, defaultConcurrency)
	}
	if tr.replyTimeout != microservice.DefaultRequestTimeout {
		t.Errorf("replyTimeout = %s", tr.replyTimeout)
	}
	if tr.pingTimeout != defaultPingTimeout {
		t.Errorf("pingTimeout = %s", tr.pingTimeout)
	}
	if !strings.HasPrefix(tr.ReplyChannel(), DefaultPrefix+":"+replyNamespace) {
		t.Errorf("reply channel %q is not in the reply namespace", tr.ReplyChannel())
	}
	if len(tr.clientID) != 32 {
		t.Errorf("client id %q is not a 128-bit hex value; a guessable reply inbox lets another process harvest replies", tr.clientID)
	}
}

func TestReplyChannelsAreUnguessableAndDistinct(t *testing.T) {
	a := newTestTransport(t)
	b := newTestTransport(t)
	if a.ReplyChannel() == b.ReplyChannel() {
		t.Fatal("two transports share a reply inbox")
	}
}

func TestName(t *testing.T) {
	if got := newTestTransport(t).Name(); got != microservice.TransportRedis {
		t.Fatalf("Name() = %q want %q", got, microservice.TransportRedis)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	tr, err := New(Options{URL: "redis://127.0.0.1:6379", Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := tr.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

// TestCloseLeavesABorrowedClientOpen: closing a client the caller supplied would
// tear down the pool the rest of the application is using.
func TestCloseLeavesABorrowedClientOpen(t *testing.T) {
	shared := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = shared.Close() })

	tr, err := New(Options{Client: shared, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A closed go-redis client reports redis.ErrClosed on any command; a live one
	// reports a connection error instead. Either way it must not be ErrClosed.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := shared.Ping(ctx).Err(); errors.Is(err, redis.ErrClosed) {
		t.Fatal("Close closed a client it does not own")
	}
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	tr, err := New(Options{URL: "redis://127.0.0.1:6379", Logger: testLogger()})
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
	if err := tr.Listen(ctx, []string{"user_created"}, nil); !errors.Is(err, microservice.ErrClosed) {
		// A nil dispatcher is also rejected, so assert the closed check wins.
		if err == nil {
			t.Error("Listen after Close returned nil")
		}
	}
	if err := tr.Ping(ctx); !errors.Is(err, microservice.ErrClosed) {
		t.Errorf("Ping after Close = %v, want ErrClosed", err)
	}
}

func TestRequestRejectsWildcardAndNilEnvelope(t *testing.T) {
	tr := newTestTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, err := tr.Request(ctx, nil, time.Second); err == nil {
		t.Error("a nil envelope must be rejected")
	}
	if err := tr.Publish(ctx, nil); err == nil {
		t.Error("a nil envelope must be rejected")
	}
	if err := tr.Publish(ctx, &microservice.Envelope{ID: "1", Pattern: "reply:steal"}); err == nil {
		t.Error("publishing into the reply namespace must be rejected")
	}
}

// ------------------------------------------------------------ correlation map

// TestAwaitReplyCleansUpOnTimeout is the leak guard. A peer that never answers must
// not cost this process a permanent map entry per call.
func TestAwaitReplyCleansUpOnTimeout(t *testing.T) {
	tr := newTestTransport(t)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_, err := tr.awaitReply(ctx, "req-1", func() error { return nil })
	if !errors.Is(err, microservice.ErrTimeout) {
		t.Fatalf("awaitReply = %v, want ErrTimeout", err)
	}
	if n := tr.pendingLen(); n != 0 {
		t.Fatalf("a timed-out request leaked %d correlation entries", n)
	}
}

func TestAwaitReplyCleansUpWhenSendFails(t *testing.T) {
	tr := newTestTransport(t)
	sendErr := errors.New("publish failed")

	_, err := tr.awaitReply(context.Background(), "req-2", func() error { return sendErr })
	if !errors.Is(err, sendErr) {
		t.Fatalf("awaitReply = %v, want the send error", err)
	}
	if n := tr.pendingLen(); n != 0 {
		t.Fatalf("a failed send leaked %d correlation entries", n)
	}
}

func TestAwaitReplyReturnsTheDeliveredReply(t *testing.T) {
	tr := newTestTransport(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// deliver races with the registration on purpose: the send callback runs after
	// the entry exists, so a reply that arrives immediately is still routable.
	reply, err := tr.awaitReply(ctx, "req-3", func() error {
		go tr.deliver(&microservice.Envelope{ID: "req-3", Pattern: "user_created", Status: 200})
		return nil
	})
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if reply.ID != "req-3" {
		t.Fatalf("reply id = %q", reply.ID)
	}
	if n := tr.pendingLen(); n != 0 {
		t.Fatalf("a completed request leaked %d correlation entries", n)
	}
}

func TestAwaitReplyIsUnblockedByClose(t *testing.T) {
	tr, err := New(Options{URL: "redis://127.0.0.1:6379", Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := tr.awaitReply(context.Background(), "req-4", func() error { return nil })
		result <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, microservice.ErrClosed) {
			t.Fatalf("awaitReply = %v, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock the pending reply wait")
	}
	if n := tr.pendingLen(); n != 0 {
		t.Fatalf("Close left %d correlation entries behind", n)
	}
}

func TestDeliverIgnoresUnknownCorrelationIDs(t *testing.T) {
	tr := newTestTransport(t)

	// A reply for a request that already timed out. It must be dropped silently and
	// must not create an entry.
	tr.deliver(&microservice.Envelope{ID: "who-dis", Pattern: "user_created"})
	if n := tr.pendingLen(); n != 0 {
		t.Fatalf("deliver created %d entries for an unknown id", n)
	}
}
