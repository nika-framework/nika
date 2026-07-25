// Package cache provides a small driver-agnostic cache abstraction plus Redis,
// file and in-memory implementations.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/nika-framework/nika"
)

type Cache struct {
	Provider Provider
}

type Config struct {
	Driver string // "redis", "file" or "memory"
	URL    string // Redis DSN for "redis"; directory path for "file"; ignored for "memory"

	// CleanupInterval opts the file and memory providers into a background
	// janitor that evicts expired entries. Left at zero, entries are only
	// reclaimed when they are read, so a cache keyed by anything unbounded grows
	// without limit. Ignored by the Redis driver, which expires keys itself.
	CleanupInterval time.Duration
}

// Setup builds the configured provider, registers it in the DI container both as
// *Cache and under the Provider interface, and arranges for it to be closed on
// shutdown.
func Setup(app *nika.App, cfg Config) (*Cache, error) {
	var provider Provider

	switch cfg.Driver {

	case "redis":
		redisProvider, err := NewRedisProvider(cfg.URL)
		if err != nil {
			return nil, err
		}
		provider = redisProvider

	case "file":
		fileProvider, err := NewFileProvider(cfg.URL)
		if err != nil {
			return nil, err
		}
		fileProvider.StartJanitor(cfg.CleanupInterval)
		provider = fileProvider

	case "memory":
		memoryProvider := NewMemoryProvider()
		memoryProvider.StartJanitor(cfg.CleanupInterval)
		provider = memoryProvider

	case "memcached":
		return nil, fmt.Errorf("memcached provider not implemented")

	default:
		return nil, fmt.Errorf("unknown cache driver: %s", cfg.Driver)
	}

	cache := &Cache{
		Provider: provider,
	}

	app.RegisterSingleton(cache)

	// Also register under the interface so consumers can depend on cache.Provider
	// and be handed a MemoryProvider in tests without touching their code.
	nika.RegisterSingletonAs[Provider](app, provider)

	// Closing on shutdown both stops the janitor goroutine and drains the Redis
	// connection pool; without it a repeatedly restarted app leaks both.
	app.OnShutdown(func(context.Context) error {
		return provider.Close()
	})

	return cache, nil
}
