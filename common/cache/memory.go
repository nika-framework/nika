package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// memoryItem is one in-process entry. A zero expiresAt means "no expiry".
type memoryItem struct {
	value     string
	expiresAt time.Time
}

func (i memoryItem) expired() bool {
	return !i.expiresAt.IsZero() && !time.Now().Before(i.expiresAt)
}

// MemoryProvider is an in-process cache.
//
// It exists for two reasons: it makes every consumer of Provider testable without
// a Redis server, and it is the right default for single-instance deployments and
// for caches that are cheap to rebuild. Because it holds no shared state it must
// not be used where several instances need to agree — session storage behind more
// than one replica, or a distributed lock.
type MemoryProvider struct {
	mu    sync.RWMutex
	items map[string]memoryItem

	closeOnce sync.Once
	stopMu    sync.Mutex
	stop      context.CancelFunc
	janitorWG sync.WaitGroup
}

// NewMemoryProvider returns an empty in-process cache.
func NewMemoryProvider() *MemoryProvider {
	return &MemoryProvider{items: make(map[string]memoryItem)}
}

func deadline(exp time.Duration) time.Time {
	if exp <= 0 {
		return time.Time{}
	}
	return time.Now().Add(exp)
}

func (m *MemoryProvider) Set(ctx context.Context, key string, value any, exp time.Duration) error {
	if key == "" {
		return ErrKeyEmpty
	}
	str, err := marshalValue(value)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = make(map[string]memoryItem)
	}
	m.items[key] = memoryItem{value: str, expiresAt: deadline(exp)}
	return nil
}

// SetNX stores value only when key is absent or its entry has already expired.
//
// The expiry check happens under the same write lock as the store, so an expired
// entry is reclaimed atomically and a lock whose holder died is re-acquirable —
// the correctness property the file provider cannot offer.
func (m *MemoryProvider) SetNX(ctx context.Context, key string, value any, exp time.Duration) (bool, error) {
	if key == "" {
		return false, ErrKeyEmpty
	}
	str, err := marshalValue(value)
	if err != nil {
		return false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = make(map[string]memoryItem)
	}
	if existing, ok := m.items[key]; ok && !existing.expired() {
		return false, nil
	}
	m.items[key] = memoryItem{value: str, expiresAt: deadline(exp)}
	return true, nil
}

func (m *MemoryProvider) Get(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrKeyEmpty
	}

	m.mu.RLock()
	item, ok := m.items[key]
	m.mu.RUnlock()

	if !ok {
		return "", ErrNotFound
	}
	if item.expired() {
		// Drop it eagerly so a read-heavy key does not keep dead memory alive
		// between janitor ticks. Re-check under the write lock: another goroutine
		// may have replaced the entry in the gap.
		m.mu.Lock()
		if current, still := m.items[key]; still && current.expired() {
			delete(m.items, key)
		}
		m.mu.Unlock()
		return "", ErrNotFound
	}
	return item.value, nil
}

// Delete removes key. Removing an absent key is not an error, matching Redis DEL.
func (m *MemoryProvider) Delete(ctx context.Context, key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	m.mu.Lock()
	delete(m.items, key)
	m.mu.Unlock()
	return nil
}

// Increment adds delta to the counter at key under the write lock, so concurrent
// callers cannot lose an update. An expired counter restarts from zero, and a live
// one keeps its original deadline.
func (m *MemoryProvider) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = make(map[string]memoryItem)
	}

	var current int64
	var expiresAt time.Time

	if existing, ok := m.items[key]; ok && !existing.expired() {
		parsed, err := strconv.ParseInt(existing.value, 10, 64)
		if err != nil {
			return 0, errors.New("cache: value at key is not an integer")
		}
		current = parsed
		// Preserve the window: a counter whose TTL restarted on every increment
		// would never expire under sustained traffic.
		expiresAt = existing.expiresAt
	}

	next := current + delta
	m.items[key] = memoryItem{value: strconv.FormatInt(next, 10), expiresAt: expiresAt}
	return next, nil
}

func (m *MemoryProvider) TTL(ctx context.Context, key string) (time.Duration, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}

	m.mu.RLock()
	item, ok := m.items[key]
	m.mu.RUnlock()

	if !ok || item.expired() {
		return 0, ErrNotFound
	}
	if item.expiresAt.IsZero() {
		return NoExpiry, nil
	}
	return time.Until(item.expiresAt), nil
}

// Len returns the number of entries currently held, expired ones included. Useful
// for asserting that the janitor actually reclaims memory.
func (m *MemoryProvider) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// StartJanitor begins periodic eviction of expired entries and reports whether it
// started one.
//
// Get evicts lazily, but a key that is written and never read again would pin its
// value forever, so a long-running process needs the sweep to bound memory. Close
// stops it.
func (m *MemoryProvider) StartJanitor(interval time.Duration) bool {
	if interval <= 0 {
		return false
	}

	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	if m.stop != nil {
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel

	m.janitorWG.Add(1)
	go func() {
		defer m.janitorWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.DeleteExpired()
			}
		}
	}()
	return true
}

// DeleteExpired evicts every expired entry and returns how many it evicted.
func (m *MemoryProvider) DeleteExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for key, item := range m.items {
		if item.expired() {
			delete(m.items, key)
			removed++
		}
	}
	return removed
}

// Ping always succeeds: there is no connection to check.
func (m *MemoryProvider) Ping(ctx context.Context) error { return nil }

// Close stops the janitor and releases every entry. Safe to call repeatedly; the
// waitgroup guarantees the goroutine is gone before Close returns.
func (m *MemoryProvider) Close() error {
	m.closeOnce.Do(func() {
		m.stopMu.Lock()
		stop := m.stop
		m.stopMu.Unlock()
		if stop != nil {
			stop()
		}
		m.janitorWG.Wait()

		m.mu.Lock()
		m.items = make(map[string]memoryItem)
		m.mu.Unlock()
	})
	return nil
}
