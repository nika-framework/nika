# Rate Limiting

Rate limiting protects your API from abuse by limiting the number of requests a client can make in a given time period.

Nika provides rate limiting through the `common/ratelimit` package. It is built on top of `github.com/ulule/limiter/v3` and supports in-memory and Redis-backed stores.

## Setup

Pass the limiter parameters when calling `Setup()`:

```go
import (
    "time"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/ratelimit"
)

func main() {
    app := nika.NewApp()

    _, err := ratelimit.Setup(app, ratelimit.Config{
        Requests: 100,
        Window:   time.Minute,
        Driver:   ratelimit.DriverMemory,
        Message:  "Too many requests",
    })
    if err != nil {
        panic(err)
    }

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

`Setup()` registers the limiter in the DI container and installs the middleware globally.

## Options

| Option | Description |
|--------|-------------|
| `Requests` | Maximum number of requests allowed in the window |
| `Window` | Time window, such as `time.Minute` |
| `Driver` | `ratelimit.DriverMemory` or `ratelimit.DriverRedis` |
| `Prefix` | Store key prefix |
| `StatusCode` | Response status when blocked, defaults to `429` |
| `Message` | Response message when blocked |
| `CleanupInterval` | How often expired visitors are removed |
| `KeyFunc` | Optional function for identifying clients |
| `Skip` | Optional function for bypassing the limiter |
| `RedisClient` | Redis client for the Redis driver |
| `Store` | Custom `limiter.Store` implementation |
| `LimiterOptions` | Advanced options passed to the underlying limiter |

## Custom Key

By default, clients are identified by IP address. You can provide a custom `KeyFunc`:

```go
ratelimit.Setup(app, ratelimit.Config{
    Requests: 10,
    Window:   time.Minute,
    KeyFunc: func(c *gin.Context) string {
        return c.GetHeader("X-API-Key")
    },
})
```

## Redis Store

For multi-instance deployments, use Redis so all app instances share the same limits:

```go
redisClient := redis.NewClient(&redis.Options{
    Addr: "localhost:6379",
})

ratelimit.Setup(app, ratelimit.Config{
    Requests:    100,
    Window:      time.Minute,
    Driver:      ratelimit.DriverRedis,
    RedisClient: redisClient,
    Prefix:      "nika_ratelimit",
})
```

## Status

| Feature | Status |
|---------|--------|
| In-memory rate limiting | ✅ Implemented |
| Redis-backed rate limiting | ✅ Implemented |
| Per-route rate limits | ✅ Use `Skip` or `Middleware()` directly |
| Rate limit headers | ✅ Implemented |
| Built-in rate limiter | ✅ Implemented |
| Custom key function | ✅ Implemented |
| Conditional bypass (`Skip`) | ✅ Implemented |

## Skipping Certain Routes

The `Skip` option lets you bypass the limiter for specific requests (for example, health checks or static assets):

```go
ratelimit.Setup(app, ratelimit.Config{
    Requests: 100,
    Window:   time.Minute,
    Driver:   ratelimit.DriverMemory,
    Skip: func(c *gin.Context) bool {
        // Don't rate-limit health checks
        return c.Request.URL.Path == "/health"
    },
})
```

## Error Response Format

When the limit is reached or an internal error occurs, the limiter responds with the standard Nika error shape:

```json
{
  "success": false,
  "error": {
    "code": 429,
    "message": "RATE_LIMIT_ERROR",
    "details": [
      { "field": "rate_limit", "message": "Too many requests" }
    ]
  }
}
```

You can customize the status code (`StatusCode`) and the message (`Message`) in the config.
