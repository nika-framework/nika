package microservice

import (
	"fmt"
	"net/url"
	"sort"
	"sync"
)

// Route binds one declared pattern to the handler that serves it.
type Route struct {
	// Pattern is the subject or wildcard the handler answers.
	Pattern Pattern

	// Transport names the transport this route listens on.
	Transport string

	// Controller and Field identify the declaring code, for error messages.
	Controller string
	Field      string

	// Guards are the guard tag specs applied before the handler.
	Guards []string

	// path is the internal route the message is dispatched through. It is a
	// generated token, never derived from the pattern, so no pattern character
	// can affect URL parsing.
	path string

	// url is the pre-parsed form of path, copied per dispatch to avoid parsing a
	// static string on every message.
	url *url.URL

	rank specificity
}

// String renders the route for logs.
func (r *Route) String() string {
	if r.Controller == "" {
		return fmt.Sprintf("%s:%s", r.Transport, r.Pattern)
	}
	return fmt.Sprintf("%s:%s → %s.%s", r.Transport, r.Pattern, r.Controller, r.Field)
}

// Router resolves an inbound literal subject to the most specific registered
// pattern.
//
// Exact subjects resolve through a map. Wildcards are held in a slice kept
// sorted by specificity, so the first match found is already the best one and
// there is no need to score every candidate. Resolved wildcard lookups are
// memoised because real traffic repeats a small set of subjects (`user_23`,
// `user_24`, …) far less often than it repeats the *shape* of them — the cache is
// bounded so a hostile publisher cannot grow it without limit.
type Router struct {
	mu    sync.RWMutex
	exact map[string]*Route
	wild  []*Route

	cacheMu sync.RWMutex
	cache   map[string]*Route
}

// maxRouteCacheEntries bounds the wildcard resolution cache. A publisher that
// invents a new subject per message must not be able to grow it unboundedly.
const maxRouteCacheEntries = 4096

// NewRouter returns an empty router.
func NewRouter() *Router {
	return &Router{
		exact: make(map[string]*Route),
		cache: make(map[string]*Route),
	}
}

// Add registers a route, rejecting duplicates and invalid patterns.
func (r *Router) Add(route *Route) error {
	if route == nil {
		return fmt.Errorf("microservice: cannot register a nil route")
	}
	if err := route.Pattern.Validate(); err != nil {
		return fmt.Errorf("microservice: %s: %w", route, err)
	}

	route.rank = rank(route.Pattern)

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing := r.find(route.Pattern); existing != nil {
		return fmt.Errorf(
			"microservice: pattern %q is already handled by %s (duplicate on %s)",
			route.Pattern, existing, route,
		)
	}

	if route.Pattern.IsWildcard() {
		r.wild = append(r.wild, route)
		sort.SliceStable(r.wild, func(i, j int) bool {
			return r.wild[i].rank.moreSpecificThan(r.wild[j].rank)
		})
	} else {
		r.exact[string(route.Pattern)] = route
	}

	r.invalidateCache()
	return nil
}

// find returns the route registered for exactly this pattern, if any. Callers
// must hold at least a read lock.
func (r *Router) find(pattern Pattern) *Route {
	if route, ok := r.exact[string(pattern)]; ok {
		return route
	}
	for _, route := range r.wild {
		if route.Pattern == pattern {
			return route
		}
	}
	return nil
}

// Resolve returns the best-matching route for a literal subject.
func (r *Router) Resolve(subject string) (*Route, bool) {
	r.mu.RLock()
	if route, ok := r.exact[subject]; ok {
		r.mu.RUnlock()
		return route, true
	}
	hasWildcards := len(r.wild) > 0
	r.mu.RUnlock()

	if !hasWildcards {
		return nil, false
	}

	r.cacheMu.RLock()
	cached, cachedOK := r.cache[subject]
	r.cacheMu.RUnlock()
	if cachedOK {
		return cached, cached != nil
	}

	r.mu.RLock()
	var match *Route
	for _, route := range r.wild {
		if route.Pattern.Match(subject) {
			match = route
			break // wild is specificity-sorted, so the first hit is the best
		}
	}
	r.mu.RUnlock()

	r.cacheMu.Lock()
	if len(r.cache) >= maxRouteCacheEntries {
		// Simple reset rather than an LRU: the working set is small, and a reset
		// costs one re-resolution per live subject instead of per-entry
		// bookkeeping on every lookup.
		r.cache = make(map[string]*Route, maxRouteCacheEntries)
	}
	r.cache[subject] = match
	r.cacheMu.Unlock()

	return match, match != nil
}

// Patterns returns every registered pattern, most specific first.
func (r *Router) Patterns() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.exact)+len(r.wild))
	for pattern := range r.exact {
		out = append(out, pattern)
	}
	sort.Strings(out)
	for _, route := range r.wild {
		out = append(out, string(route.Pattern))
	}
	return out
}

// Routes returns every registered route.
func (r *Router) Routes() []*Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Route, 0, len(r.exact)+len(r.wild))
	for _, route := range r.exact {
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return append(out, r.wild...)
}

// Len returns the number of registered routes.
func (r *Router) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.exact) + len(r.wild)
}

func (r *Router) invalidateCache() {
	r.cacheMu.Lock()
	r.cache = make(map[string]*Route, maxRouteCacheEntries)
	r.cacheMu.Unlock()
}
