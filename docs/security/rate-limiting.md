# Rate Limiting

> **Coming Soon** — Built-in rate limiting is not yet implemented.

Rate limiting protects your API from abuse by limiting the number of requests a client can make in a given time period.

## Current Approach

Use `gin-contrib/limiter` or implement custom middleware:

```bash
go get github.com/ulule/limiter/v3
go get github.com/ulule/limiter/v3/drivers/middleware/gin
go get github.com/ulule/limiter/v3/drivers/store/redis
```

```go
import (
    "github.com/ulule/limiter/v3"
    "github.com/ulule/limiter/v3/drivers/middleware/gin"
    "github.com/ulule/limiter/v3/drivers/store/redis"
)

func main() {
    app := nika.NewApp()

    // Define rate limit: 100 requests per minute per IP
    rate, _ := limiter.NewRateFromFormatted("100-M")

    // Create store (Redis-backed)
    store, _ := redis.NewStoreWithOptions(
        redisClient,
        limiter.StoreOptions{
            Prefix: "nika_ratelimit",
        },
    )

    // Create middleware
    middleware := gin.NewMiddleware(limiter.New(store, rate))
    app.Use(middleware)

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Simple In-Memory Rate Limiter

```go
package middleware

import (
    "net/http"
    "sync"
    "time"
    "github.com/gin-gonic/gin"
)

type visitor struct {
    count    int
    lastSeen time.Time
}

type RateLimiter struct {
    mu       sync.Mutex
    visitors map[string]*visitor
    rate     int           // max requests
    window   time.Duration // time window
}

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
    rl := &RateLimiter{
        visitors: make(map[string]*visitor),
        rate:     rate,
        window:   window,
    }
    // Cleanup old entries
    go rl.cleanup()
    return rl
}

func (rl *RateLimiter) Limit() gin.HandlerFunc {
    return func(c *gin.Context) {
        ip := c.ClientIP()

        rl.mu.Lock()
        v, exists := rl.visitors[ip]
        if !exists {
            rl.visitors[ip] = &visitor{count: 1, lastSeen: time.Now()}
            rl.mu.Unlock()
            c.Next()
            return
        }

        if time.Since(v.lastSeen) > rl.window {
            v.count = 1
            v.lastSeen = time.Now()
        } else {
            v.count++
        }

        if v.count > rl.rate {
            rl.mu.Unlock()
            c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
                "error": "Rate limit exceeded",
            })
            return
        }

        rl.mu.Unlock()
        c.Next()
    }
}

func (rl *RateLimiter) cleanup() {
    for {
        time.Sleep(time.Minute)
        rl.mu.Lock()
        for ip, v := range rl.visitors {
            if time.Since(v.lastSeen) > rl.window*2 {
                delete(rl.visitors, ip)
            }
        }
        rl.mu.Unlock()
    }
}

// Usage
app.Use(NewRateLimiter(100, time.Minute).Limit())
```

## Status

| Feature | Status |
|---------|--------|
| In-memory rate limiting | ⏳ Planned |
| Redis-backed rate limiting | ⏳ Planned |
| Per-route rate limits | ⏳ Planned |
| Rate limit headers | ⏳ Planned |
| Built-in rate limiter | ⏳ Planned |
