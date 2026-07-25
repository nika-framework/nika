package microservice

import (
	"strings"
	"testing"
	"time"
)

func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}

// route builds a mounted route so Resolve has something to return.
func route(pattern string) *Route {
	return &Route{
		Pattern:    Pattern(pattern),
		Transport:  TransportMemory,
		Controller: "TestController",
		Field:      "Handler",
		path:       "/_nika/message/" + strings.ReplaceAll(pattern, "*", "star"),
	}
}

func mustAdd(t *testing.T, router *Router, patterns ...string) {
	t.Helper()
	for _, pattern := range patterns {
		if err := router.Add(route(pattern)); err != nil {
			t.Fatalf("Add(%q) returned %v", pattern, err)
		}
	}
}

// TestRouterResolvesTheDeclaredScenario is the framework's headline behaviour:
// three handlers on "user_created", "user_*" and "users", and a client that just
// sends "user_created", "user_23" and "users" reaches them in that order — even
// though "user_created" also matches "user_*".
func TestRouterResolvesTheDeclaredScenario(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_created", "user_*", "users")

	tests := []struct {
		subject     string
		wantPattern string
	}{
		{subject: "user_created", wantPattern: "user_created"},
		{subject: "user_23", wantPattern: "user_*"},
		{subject: "users", wantPattern: "users"},
		{subject: "user_", wantPattern: "user_*"},
		{subject: "user_deleted", wantPattern: "user_*"},
	}

	for _, test := range tests {
		t.Run(test.subject, func(t *testing.T) {
			resolved, found := router.Resolve(test.subject)
			if !found {
				t.Fatalf("Resolve(%q) found nothing; registered: %v", test.subject, router.Patterns())
			}
			if string(resolved.Pattern) != test.wantPattern {
				t.Errorf("Resolve(%q) → %q, want %q", test.subject, resolved.Pattern, test.wantPattern)
			}
		})
	}
}

// TestRouterPrecedenceIsIndependentOfRegistrationOrder is the regression guard:
// the wildcard list is specificity-sorted, so adding "user_*" before or after
// "user_created" must not change which one serves "user_created".
func TestRouterPrecedenceIsIndependentOfRegistrationOrder(t *testing.T) {
	orders := [][]string{
		{"user_created", "user_*"},
		{"user_*", "user_created"},
		{"user_*", "users", "user_created"},
	}

	for _, order := range orders {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			router := NewRouter()
			mustAdd(t, router, order...)

			resolved, found := router.Resolve("user_created")
			if !found {
				t.Fatal("Resolve(\"user_created\") found nothing")
			}
			if resolved.Pattern != "user_created" {
				t.Errorf("with registration order %v, \"user_created\" resolved to %q",
					order, resolved.Pattern)
			}
		})
	}
}

func TestRouterPrefersTheMoreSpecificWildcard(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_*", "user_admin_*", "*")

	tests := map[string]string{
		"user_admin_created": "user_admin_*",
		"user_created":       "user_*",
		"order_created":      "*",
	}

	for subject, wantPattern := range tests {
		resolved, found := router.Resolve(subject)
		if !found {
			t.Fatalf("Resolve(%q) found nothing", subject)
		}
		if string(resolved.Pattern) != wantPattern {
			t.Errorf("Resolve(%q) → %q, want %q", subject, resolved.Pattern, wantPattern)
		}
	}
}

func TestRouterRejectsDuplicates(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_created", "user_*")

	t.Run("duplicate literal", func(t *testing.T) {
		err := router.Add(route("user_created"))
		if err == nil {
			t.Fatal("Add of a duplicate literal returned nil, want an error")
		}
		if !strings.Contains(err.Error(), "already handled") {
			t.Errorf("error = %v, want it to name the existing handler", err)
		}
	})

	t.Run("duplicate wildcard", func(t *testing.T) {
		if err := router.Add(route("user_*")); err == nil {
			t.Fatal("Add of a duplicate wildcard returned nil, want an error")
		}
	})
}

func TestRouterRejectsInvalidPatterns(t *testing.T) {
	router := NewRouter()

	if err := router.Add(route("")); err == nil {
		t.Error("Add of an empty pattern returned nil, want an error")
	}
	if err := router.Add(nil); err == nil {
		t.Error("Add(nil) returned nil, want an error")
	}
}

func TestRouterMissesAreReported(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_created")

	if resolved, found := router.Resolve("order_created"); found {
		t.Errorf("Resolve(\"order_created\") → %q, want no match", resolved.Pattern)
	}
	// With no wildcards registered, a miss must not consult the wildcard list at
	// all — this is the fast path for the common literal-only service.
	if router.Len() != 1 {
		t.Errorf("Len() = %d, want 1", router.Len())
	}
}

// TestRouterCachesNegativeResults matters because a hostile publisher can invent
// a fresh subject per message; a cache that only stores hits would re-scan the
// wildcard list forever, and one that grows without bound is a memory leak.
func TestRouterCachesNegativeResults(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_*")

	if _, found := router.Resolve("order_1"); found {
		t.Fatal("Resolve(\"order_1\") matched, want no match")
	}
	// Second lookup must come from the cache and still report a miss.
	if _, found := router.Resolve("order_1"); found {
		t.Error("the cached negative result reported a match")
	}
}

func TestRouterCacheIsBounded(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_*")

	for i := 0; i < maxRouteCacheEntries+100; i++ {
		router.Resolve("order_" + itoa(i))
	}

	router.cacheMu.RLock()
	size := len(router.cache)
	router.cacheMu.RUnlock()

	if size > maxRouteCacheEntries {
		t.Errorf("the resolution cache holds %d entries, above the %d cap",
			size, maxRouteCacheEntries)
	}
}

// TestRouterCacheIsInvalidatedOnAdd catches the subtle failure where a subject
// resolved before a more specific pattern was registered keeps resolving to the
// stale, less specific handler.
func TestRouterCacheIsInvalidatedOnAdd(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_*")

	if resolved, _ := router.Resolve("user_created"); resolved.Pattern != "user_*" {
		t.Fatalf("initial resolve → %q, want \"user_*\"", resolved.Pattern)
	}

	mustAdd(t, router, "user_created")

	resolved, found := router.Resolve("user_created")
	if !found {
		t.Fatal("Resolve found nothing after registering the exact pattern")
	}
	if resolved.Pattern != "user_created" {
		t.Errorf("after registering the exact pattern, resolve → %q, want \"user_created\" (stale cache)",
			resolved.Pattern)
	}
}

func TestRouterConcurrentResolveIsRaceFree(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_created", "user_*", "order_*", "*")

	subjects := []string{"user_created", "user_9", "order_1", "anything"}

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for n := 0; n < 200; n++ {
				router.Resolve(subjects[(i+n)%len(subjects)])
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestRouterPatternsListsExactThenWildcard(t *testing.T) {
	router := NewRouter()
	mustAdd(t, router, "user_*", "users", "user_created", "*")

	patterns := router.Patterns()
	if len(patterns) != 4 {
		t.Fatalf("Patterns() = %v, want 4 entries", patterns)
	}

	// Wildcards come last and in specificity order, so a startup log reads as the
	// dispatch order.
	if patterns[len(patterns)-1] != "*" {
		t.Errorf("Patterns() = %v, want the catch-all last", patterns)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
