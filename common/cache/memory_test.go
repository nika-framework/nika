package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// providerConformance is the behaviour every Provider must exhibit. It is shared
// by the memory and file suites so the two drivers cannot drift: a caller that
// swaps one for the other should not have to change a line.
func providerConformance(t *testing.T, newProvider func(t *testing.T) Provider) {
	t.Helper()
	ctx := context.Background()

	t.Run("get missing key reports ErrNotFound", func(t *testing.T) {
		p := newProvider(t)
		got, err := p.Get(ctx, "absent")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get error = %v, want ErrNotFound", err)
		}
		if got != "" {
			t.Fatalf("Get value = %q, want empty string alongside ErrNotFound", got)
		}
	})

	t.Run("round trips non-string values", func(t *testing.T) {
		type payload struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}

		cases := []struct {
			name  string
			value any
			want  string
		}{
			{"string", "plain", "plain"},
			{"bytes", []byte("raw"), "raw"},
			{"int", 42, "42"},
			{"negative int", -7, "-7"},
			{"int64", int64(9007199254740993), "9007199254740993"},
			{"float", 1.5, "1.5"},
			{"bool", true, "true"},
			{"struct", payload{ID: 3, Name: "nika"}, `{"id":3,"name":"nika"}`},
			{"pointer to struct", &payload{ID: 4, Name: "x"}, `{"id":4,"name":"x"}`},
			{"slice", []int{1, 2, 3}, "[1,2,3]"},
			{"map", map[string]int{"a": 1}, `{"a":1}`},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p := newProvider(t)
				// The pre-fix providers did value.(string) here, which panicked and
				// took the whole process down for every case below "bytes".
				if err := p.Set(ctx, "k", tc.value, time.Minute); err != nil {
					t.Fatalf("Set(%T) error = %v, want nil", tc.value, err)
				}
				got, err := p.Get(ctx, "k")
				if err != nil {
					t.Fatalf("Get error = %v, want nil", err)
				}
				if got != tc.want {
					t.Fatalf("Get = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("rejects a nil value instead of panicking", func(t *testing.T) {
		p := newProvider(t)
		if err := p.Set(ctx, "k", nil, time.Minute); err == nil {
			t.Fatal("Set(nil) error = nil, want an error")
		}
	})

	t.Run("rejects an empty key", func(t *testing.T) {
		p := newProvider(t)

		if err := p.Set(ctx, "", "v", time.Minute); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("Set error = %v, want ErrKeyEmpty", err)
		}
		if _, err := p.Get(ctx, ""); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("Get error = %v, want ErrKeyEmpty", err)
		}
		if _, err := p.SetNX(ctx, "", "v", time.Minute); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("SetNX error = %v, want ErrKeyEmpty", err)
		}
		if err := p.Delete(ctx, ""); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("Delete error = %v, want ErrKeyEmpty", err)
		}
		if _, err := p.Increment(ctx, "", 1); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("Increment error = %v, want ErrKeyEmpty", err)
		}
		if _, err := p.TTL(ctx, ""); !errors.Is(err, ErrKeyEmpty) {
			t.Fatalf("TTL error = %v, want ErrKeyEmpty", err)
		}
	})

	t.Run("expired entry reads as ErrNotFound", func(t *testing.T) {
		p := newProvider(t)
		if err := p.Set(ctx, "short", "v", 100*time.Millisecond); err != nil {
			t.Fatalf("Set error = %v", err)
		}
		if _, err := p.Get(ctx, "short"); err != nil {
			t.Fatalf("Get before expiry error = %v, want nil", err)
		}

		time.Sleep(200 * time.Millisecond)

		if _, err := p.Get(ctx, "short"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after expiry error = %v, want ErrNotFound", err)
		}
	})

	t.Run("zero expiration stores without an expiry", func(t *testing.T) {
		p := newProvider(t)
		if err := p.Set(ctx, "forever", "v", NoExpiry); err != nil {
			t.Fatalf("Set error = %v", err)
		}

		time.Sleep(20 * time.Millisecond)

		if got, err := p.Get(ctx, "forever"); err != nil || got != "v" {
			t.Fatalf("Get = (%q, %v), want (\"v\", nil)", got, err)
		}
		ttl, err := p.TTL(ctx, "forever")
		if err != nil {
			t.Fatalf("TTL error = %v, want nil", err)
		}
		if ttl != NoExpiry {
			t.Fatalf("TTL = %v, want NoExpiry", ttl)
		}
	})

	t.Run("set overwrites an existing entry", func(t *testing.T) {
		p := newProvider(t)
		mustSet(t, p, "k", "first", time.Minute)
		mustSet(t, p, "k", "second", time.Minute)

		if got, _ := p.Get(ctx, "k"); got != "second" {
			t.Fatalf("Get = %q, want \"second\"", got)
		}
	})

	t.Run("SetNX only the first caller wins", func(t *testing.T) {
		p := newProvider(t)

		ok, err := p.SetNX(ctx, "lock", "holder-a", time.Minute)
		if err != nil || !ok {
			t.Fatalf("first SetNX = (%v, %v), want (true, nil)", ok, err)
		}

		ok, err = p.SetNX(ctx, "lock", "holder-b", time.Minute)
		if err != nil {
			t.Fatalf("second SetNX error = %v, want nil", err)
		}
		if ok {
			t.Fatal("second SetNX = true, want false: the key is already held")
		}

		if got, _ := p.Get(ctx, "lock"); got != "holder-a" {
			t.Fatalf("value = %q, want the first holder's value", got)
		}
	})

	t.Run("SetNX reacquires an expired key", func(t *testing.T) {
		p := newProvider(t)

		ok, err := p.SetNX(ctx, "lock", "dead-holder", 100*time.Millisecond)
		if err != nil || !ok {
			t.Fatalf("first SetNX = (%v, %v), want (true, nil)", ok, err)
		}

		time.Sleep(200 * time.Millisecond)

		// This is the regression that made SetNX unusable as a lock: the file
		// provider's O_EXCL reported "exists" for an entry that had already
		// expired, so a lock whose holder crashed could never be taken again.
		ok, err = p.SetNX(ctx, "lock", "new-holder", time.Minute)
		if err != nil {
			t.Fatalf("SetNX after expiry error = %v, want nil", err)
		}
		if !ok {
			t.Fatal("SetNX after expiry = false, want true: an expired lock must be reacquirable")
		}

		if got, _ := p.Get(ctx, "lock"); got != "new-holder" {
			t.Fatalf("value = %q, want \"new-holder\"", got)
		}
	})

	t.Run("SetNX contention leaves exactly one winner", func(t *testing.T) {
		p := newProvider(t)

		const goroutines = 16
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners int
		)
		start := make(chan struct{})

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ok, err := p.SetNX(ctx, "contended", "v", time.Minute)
				if err != nil {
					return
				}
				if ok {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()

		if winners != 1 {
			t.Fatalf("winners = %d, want exactly 1", winners)
		}
	})

	t.Run("Increment", func(t *testing.T) {
		p := newProvider(t)

		// An absent key counts as zero, so a rate limiter does not need to seed it.
		got, err := p.Increment(ctx, "hits", 1)
		if err != nil {
			t.Fatalf("Increment error = %v, want nil", err)
		}
		if got != 1 {
			t.Fatalf("Increment on absent key = %d, want 1", got)
		}

		if got, err = p.Increment(ctx, "hits", 4); err != nil || got != 5 {
			t.Fatalf("Increment = (%d, %v), want (5, nil)", got, err)
		}
		if got, err = p.Increment(ctx, "hits", -2); err != nil || got != 3 {
			t.Fatalf("Increment with negative delta = (%d, %v), want (3, nil)", got, err)
		}
		if value, _ := p.Get(ctx, "hits"); value != "3" {
			t.Fatalf("Get after Increment = %q, want \"3\"", value)
		}
	})

	t.Run("Increment on a non-integer value errors", func(t *testing.T) {
		p := newProvider(t)
		mustSet(t, p, "text", "not-a-number", time.Minute)

		if _, err := p.Increment(ctx, "text", 1); err == nil {
			t.Fatal("Increment error = nil, want an error")
		}
	})

	t.Run("Increment preserves the original window", func(t *testing.T) {
		p := newProvider(t)

		if _, err := p.Increment(ctx, "window", 1); err != nil {
			t.Fatalf("Increment error = %v", err)
		}
		// Seed a TTL, then confirm a later increment does not extend it: a
		// rate-limit counter that renewed its own window would never reset.
		mustSet(t, p, "window", "1", 400*time.Millisecond)
		time.Sleep(150 * time.Millisecond)
		if _, err := p.Increment(ctx, "window", 1); err != nil {
			t.Fatalf("Increment error = %v", err)
		}

		ttl, err := p.TTL(ctx, "window")
		if err != nil {
			t.Fatalf("TTL error = %v, want nil", err)
		}
		if ttl > 300*time.Millisecond {
			t.Fatalf("TTL = %v, want it not to have been extended past the original window", ttl)
		}
	})

	t.Run("TTL", func(t *testing.T) {
		p := newProvider(t)
		mustSet(t, p, "k", "v", time.Minute)

		ttl, err := p.TTL(ctx, "k")
		if err != nil {
			t.Fatalf("TTL error = %v, want nil", err)
		}
		if ttl <= 0 || ttl > time.Minute {
			t.Fatalf("TTL = %v, want a positive value no greater than a minute", ttl)
		}

		if _, err := p.TTL(ctx, "absent"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("TTL on absent key = %v, want ErrNotFound", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		p := newProvider(t)
		mustSet(t, p, "k", "v", time.Minute)

		if err := p.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete error = %v, want nil", err)
		}
		if _, err := p.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
		}
		// Deleting an absent key mirrors Redis DEL and must not be an error.
		if err := p.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete of absent key = %v, want nil", err)
		}
	})

	t.Run("Ping and Close", func(t *testing.T) {
		p := newProvider(t)
		if err := p.Ping(ctx); err != nil {
			t.Fatalf("Ping error = %v, want nil", err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("Close error = %v, want nil", err)
		}
		// Close must be idempotent so a shutdown hook and a test cleanup can both
		// call it.
		if err := p.Close(); err != nil {
			t.Fatalf("second Close error = %v, want nil", err)
		}
	})
}

func mustSet(t *testing.T, p Provider, key string, value any, exp time.Duration) {
	t.Helper()
	if err := p.Set(context.Background(), key, value, exp); err != nil {
		t.Fatalf("Set(%q) error = %v, want nil", key, err)
	}
}

func TestMemoryProviderConformance(t *testing.T) {
	providerConformance(t, func(t *testing.T) Provider {
		p := NewMemoryProvider()
		t.Cleanup(func() { _ = p.Close() })
		return p
	})
}

func TestMemoryProviderJanitorEvictsExpiredEntries(t *testing.T) {
	ctx := context.Background()
	p := NewMemoryProvider()
	t.Cleanup(func() { _ = p.Close() })

	if !p.StartJanitor(10 * time.Millisecond) {
		t.Fatal("StartJanitor = false, want true")
	}
	// A second call must not start a second goroutine.
	if p.StartJanitor(10 * time.Millisecond) {
		t.Fatal("second StartJanitor = true, want false")
	}

	for _, key := range []string{"a", "b", "c"} {
		mustSet(t, p, key, "v", 30*time.Millisecond)
	}
	mustSet(t, p, "kept", "v", NoExpiry)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.Len() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := p.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1: the janitor should have evicted the three expired entries", got)
	}
	if _, err := p.Get(ctx, "kept"); err != nil {
		t.Fatalf("Get(kept) error = %v, want nil: an entry without an expiry must survive", err)
	}
}

func TestMemoryProviderStartJanitorRejectsNonPositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		p := NewMemoryProvider()
		if p.StartJanitor(interval) {
			t.Fatalf("StartJanitor(%v) = true, want false", interval)
		}
		_ = p.Close()
	}
}

func TestMemoryProviderDeleteExpired(t *testing.T) {
	p := NewMemoryProvider()
	t.Cleanup(func() { _ = p.Close() })

	mustSet(t, p, "dead", "v", time.Nanosecond)
	mustSet(t, p, "alive", "v", time.Minute)
	time.Sleep(5 * time.Millisecond)

	if removed := p.DeleteExpired(); removed != 1 {
		t.Fatalf("DeleteExpired = %d, want 1", removed)
	}
	if got := p.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestMemoryProviderConcurrentIncrementLosesNoUpdate(t *testing.T) {
	ctx := context.Background()
	p := NewMemoryProvider()
	t.Cleanup(func() { _ = p.Close() })

	const goroutines, perGoroutine = 8, 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := p.Increment(ctx, "counter", 1); err != nil {
					t.Errorf("Increment error = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := p.Get(ctx, "counter")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if want := "800"; got != want {
		t.Fatalf("counter = %q, want %q", got, want)
	}
}

func TestMemoryProviderCloseReleasesEntries(t *testing.T) {
	p := NewMemoryProvider()
	mustSet(t, p, "k", "v", time.Minute)

	if err := p.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if got := p.Len(); got != 0 {
		t.Fatalf("Len after Close = %d, want 0", got)
	}
}

// TestMemoryProviderSatisfiesInterface fails to compile rather than at runtime if
// the interface and the implementation drift.
func TestMemoryProviderSatisfiesInterface(t *testing.T) {
	var _ Provider = (*MemoryProvider)(nil)
	var _ Provider = (*FileProvider)(nil)
	var _ Provider = (*RedisProvider)(nil)
}
