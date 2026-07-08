# Caching

Nika provides a cache abstraction through the `common/cache` package with support for **Redis** and **File-based** drivers.

## Setup

```go
package main

import (
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/cache"
)

func main() {
    app := nika.NewApp()

    // Setup cache and register in DI container
    cacheInstance, err := cache.Setup(app, cache.Config{
        Driver: "redis",
        URL:    "redis://localhost:6379",
    })
    if err != nil {
        panic(err)
    }

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Cache Drivers

### Redis Driver

Connects to a Redis server using [go-redis](https://github.com/redis/go-redis).

```go
cache.Setup(app, cache.Config{
    Driver: "redis",
    URL:    "redis://localhost:6379",
    // With password:
    // URL: "redis://:password@localhost:6379/0",
})
```

### File Driver

Stores cached data as JSON files on the filesystem.

```go
cache.Setup(app, cache.Config{
    Driver: "file",
    URL:    "./cache", // Directory path
})
```

Each cached item is stored as a separate JSON file:

```json
{
  "value": "{\"name\":\"Alice\"}",
  "expires_at": "2024-01-15T10:30:00Z",
  "no_expiry": false
}
```

## Using Cache in Providers

The `*cache.Cache` instance is registered in the DI container:

```go
package src

import (
    "context"
    "time"
    "github.com/nika-framework/nika/common/cache"
)

type UserService struct {
    cache *cache.Cache
}

func NewUserService(cache *cache.Cache) *UserService {
    return &UserService{cache: cache}
}

func (s *UserService) GetUser(ctx context.Context, id string) (*User, error) {
    // Try cache first
    cached, err := s.cache.Get(ctx, "user:"+id)
    if err == nil {
        // Found in cache
        var user User
        json.Unmarshal([]byte(cached), &user)
        return &user, nil
    }

    // Cache miss — fetch from database
    user, err := s.repo.FindOneByID(ctx, id)
    if err != nil {
        return nil, err
    }

    // Store in cache for 10 minutes
    data, _ := json.Marshal(user)
    s.cache.Set(ctx, "user:"+id, string(data), 10*time.Minute)

    return user, nil
}
```

## API Reference

### `Cache.Provider` Interface

```go
type Provider interface {
    Set(ctx context.Context, key string, value any, expiration time.Duration) error
    Get(ctx context.Context, key string) (string, error)
    Delete(ctx context.Context, key string) error
    Close() error
    Ping(ctx context.Context) error
}
```

### Methods

| Method | Description |
|--------|-------------|
| `Set(ctx, key, value, ttl)` | Store a value with optional TTL |
| `Get(ctx, key)` | Retrieve a value by key |
| `Delete(ctx, key)` | Remove a value by key |
| `Close()` | Close the connection/cleanup resources |
| `Ping(ctx)` | Check connection health |

### Set Examples

```go
// Store with TTL (10 minutes)
err := cache.Set(ctx, "user:123", `{"name":"Alice"}`, 10*time.Minute)

// Store without TTL (permanent)
err := cache.Set(ctx, "config:app", `{"debug":true}`, 0)
```

## Configuration with `.env`

```go
cfg := config.Setup(app, "")

cache.Setup(app, cache.Config{
    Driver: cfg.Get("CACHE_DRIVER", "redis"),
    URL:    cfg.Get("CACHE_URL", "redis://localhost:6379"),
})
```

## Supported Drivers

| Driver | Status | Description |
|--------|--------|-------------|
| `redis` | ✅ Implemented | Redis server via go-redis |
| `file` | ✅ Implemented | JSON files on filesystem |
| `memcached` | ⏳ Not implemented | Memcached server |

## Complete Example

```go
package main

import (
    "context"
    "fmt"
    "time"
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/cache"
)

func main() {
    app := nika.NewApp()

    cacheInstance, err := cache.Setup(app, cache.Config{
        Driver: "file",
        URL:    "./.cache",
    })
    if err != nil {
        panic(err)
    }

    ctx := context.Background()

    // Set a value
    err = cacheInstance.Set(ctx, "greeting", "Hello, Nika!", 5*time.Minute)
    if err != nil {
        panic(err)
    }

    // Get a value
    val, err := cacheInstance.Get(ctx, "greeting")
    fmt.Println(val) // "Hello, Nika!"

    // Delete a value
    err = cacheInstance.Delete(ctx, "greeting")

    // Check health
    err = cacheInstance.Ping(ctx)
    fmt.Println("Cache health:", err == nil)

    // Cleanup
    cacheInstance.Close()

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```
