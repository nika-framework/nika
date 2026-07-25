# Hardening

This page lists what `nika.NewApp()` protects you from by default, what it
deliberately leaves to you, and the settings to change before going to
production.

## Defaults you get for free

| Protection | Default | Attack it prevents |
|---|---|---|
| Panic recovery | on | one nil dereference taking down the process |
| Request body cap | 10 MiB | memory exhaustion from a single streamed body |
| `ReadHeaderTimeout` | 10 s | Slowloris holding every worker |
| `IdleTimeout` | 60 s | idle keep-alive connections exhausting file descriptors |
| `MaxHeaderBytes` | 1 MiB | oversized header sets |
| Proxy trust | **none** | `X-Forwarded-For` spoofing → rate-limit and allow-list bypass |
| JSON 404/405 bodies | on | clients crashing on an unexpected content type |
| Graceful shutdown | on | dropped in-flight requests on every deploy |

### Proxy trust is the one to understand

Gin trusts every proxy out of the box, which means any client can send
`X-Forwarded-For: 1.2.3.4` and `c.ClientIP()` will believe it. Since IP-based
rate limiting, allow-lists and audit logs all read `ClientIP()`, that single
default undermines all three: an attacker rotates the header per request and the
rate limiter never sees the same client twice.

Nika starts with an empty trusted-proxy list, so `ClientIP()` returns the real
socket peer and cannot be influenced by a header.

**Behind a load balancer you must declare it**, or every request will appear to
come from the balancer and share one rate-limit bucket:

```go
app := nika.NewApp(nika.Config{
    TrustedProxies: []string{"10.0.0.0/8"},   // your balancer's range
})
```

Never use `0.0.0.0/0` — that is the unsafe default this exists to avoid. On a
managed platform, prefer the platform header instead:

```go
app := nika.NewApp(nika.Config{TrustedPlatform: gin.PlatformCloudflare})
```

## Settings to turn on

```go
app := nika.NewApp(nika.Config{
    SecurityHeaders: true,                    // nosniff, frame-deny, referrer, HSTS on TLS
    RequestID:       true,                    // correlate logs; validated, not echoed blindly
    RequestLogger:   true,                    // structured access log
    TrustedProxies:  []string{"10.0.0.0/8"},
    ReadTimeout:     30 * time.Second,        // see the caveat below
    WriteTimeout:    30 * time.Second,
})
```

`SecurityHeaders` does **not** set a Content-Security-Policy: a useful CSP is
application-specific, and a wrong one silently breaks pages. Add your own.

### The timeout caveat

`ReadTimeout` and `WriteTimeout` are **off by default**, and that is deliberate. A
whole-request deadline also applies to hijacked connections, so setting them
silently breaks WebSockets, Server-Sent Events and large file transfers — with a
failure mode ("the connection dies after exactly 30 seconds") that is
disproportionately hard to diagnose.

Turn them on when the service only serves ordinary bounded requests. Leave them
off and rely on `ReadHeaderTimeout` plus the body cap when it streams.

## Response and error handling

Every error responder in `common/response` **aborts** the handler chain. This
matters more than it sounds: a guard that rejected a request with
`response.BadRequest` used to write the error and then let the protected handler
run anyway — an authorization bypass hiding in a helper.

Error bodies never carry internals. `InternalServerError` logs the real error and
returns a generic message plus the request id, so an operator can correlate the
two without the client learning your dependency versions or file paths. The
recovery middleware does the same for panics: the stack goes to the log, never to
the response.

## Input validation

```go
if !validator.BindAndValidate(c, &dto) {
    return // the 400 or 422 envelope is already written
}
```

Bind failures are translated rather than echoed: a raw `err.Error()` from the
JSON decoder leaks Go type names and struct field names. Validation errors report
the **JSON** field name the client sent, not the Go field name, so a client can
map them back to its own form.

Useful validators beyond the defaults: `password_strong`, `no_html`,
`safe_filename`, `slug`, `ir_mobile`, `ir_national_code`, `objectid`.

## Database access

**SQL.** Values were always parameterised; identifiers now are validated and
quoted too. That closes the injection path through a filter map built from
request data:

```go
// Rejected — the key is not a valid identifier.
repo.FindOne(ctx, Filter{"1=1 OR name": "x"})
```

For anything beyond equality use the `Cond` API, whose operators are a closed
enum and therefore cannot come from a request:

```go
repo.FindByWhere(ctx, []Cond{
    {Column: "status", Op: OpIn, Value: []string{"active", "pending"}},
    {Column: "age", Op: OpGTE, Value: 18},
})
```

`UpdateMany` and `DeleteMany` reject an empty filter, because an empty filter
means *the whole table*. Use `UpdateAllUnsafe` / `DeleteAllUnsafe` when that is
genuinely what you want — the name is the warning.

`RawQuery` and `RawExec` are raw by contract. Parameterise them yourself.

**MongoDB.** `Filter` is trusted input. A filter built from a request body is a
NoSQL injection: `{"password": {"$ne": null}}` matches every user. Route user
input through the sanitiser:

```go
filter, err := repository.SanitizeUserFilter(userInput)  // rejects $-prefixed keys
```

`FindAll` applies a default 1000-row limit and pagination clamps page size at 500,
so `?perPage=100000000` cannot exhaust memory.

## Caching

Cache keys are hashed before touching the filesystem, so a key like
`../../etc/cron.d/x` cannot escape the cache directory — a cache write must never
become an arbitrary file write. Cache directories are created `0700` and entries
`0600`, because session data in a world-readable directory is a local
information leak.

Use the Redis provider for distributed locks. The file provider's `SetNX` is
correct within one process but is not a cross-process lock.

## Rate limiting

```go
ratelimit.Setup(app, ratelimit.Config{
    Requests: 100,
    Window:   time.Minute,
    Driver:   ratelimit.DriverRedis,   // memory is per-instance
    RedisClient: redisClient,
})
```

The memory driver counts per process, so N replicas allow N× the limit. Use Redis
for any multi-instance deployment.

Rate limiting keys on `ClientIP()`, which is only trustworthy because of the
proxy-trust default above. If you widen `TrustedProxies`, you widen this too.

## CORS

`AllowAllOrigins` together with `AllowCredentials` is rejected at setup. The
combination is forbidden by the CORS spec, and where a library honours it anyway
it lets **any** website read your authenticated responses. Enumerate origins
instead:

```go
cors.Setup(app, cors.Config{
    AllowOrigins:     []string{"https://app.example.com"},
    AllowCredentials: true,
})
```

## API documentation

Swagger is off in release mode by default, because a public docs endpoint
publishes your entire API surface — every route, parameter and model — to anyone
who looks. Put it behind auth if you need it in production:

```go
swagger.Setup(app, &swagger.Config{
    Enabled: ptr(true),
    Guards:  []gin.HandlerFunc{basicAuth},
})
```

## Microservices

Envelope headers arrive from a remote publisher, so the framework drops the ones
that could do harm before they reach a handler: anything containing CR/LF (header
smuggling), the body-framing headers, and `X-Forwarded-For` / `X-Real-IP` (which
would let a publisher spoof `ClientIP()`). Application headers pass through.

Decoded envelopes are capped at 8 MiB, and the TCP transport validates its length
prefix **before** allocating — a length field read from the network and used as an
allocation size is the classic remote memory-exhaustion bug.

Correlation ids are 128 bits of `crypto/rand`. On transports where replies share
a channel, a predictable id would let one client harvest another's reply.

The message engine recovers from panics by default. It has to: an escaping panic
in a consumer stops **every** subject, not just the one that failed.

gRPC requires explicit opt-in to run without TLS, so plaintext service-to-service
traffic cannot ship by accident.

## Deployment checklist

- [ ] `TrustedProxies` set to your balancer's range — never `0.0.0.0/0`
- [ ] `SecurityHeaders: true`, plus a CSP of your own
- [ ] TLS terminated, so HSTS actually applies
- [ ] Redis-backed rate limiting if you run more than one replica
- [ ] Swagger off, or behind auth
- [ ] CORS origins enumerated, not wildcarded, wherever credentials are allowed
- [ ] `ReadTimeout`/`WriteTimeout` set if you do not stream
- [ ] Shutdown hooks registered for every connection pool you open
- [ ] `go test -race ./...` green in CI
