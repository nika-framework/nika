# Microservices Overview

Nika services talk to each other through **patterns**. A handler declares the
pattern it answers on a struct tag, a client sends a pattern and a payload, and
the framework routes between them — over Redis, NATS, RabbitMQ, Kafka, gRPC or
raw TCP, chosen by configuration rather than by code.

```go
type UserController struct {
    Create   func(*gin.Context) `transport:"redis" pattern:"user_created"`
    FindOne  func(*gin.Context) `transport:"redis" pattern:"user_*"`
    ListUser func(*gin.Context) `transport:"redis" pattern:"users"`
}
```

A client that sends `user_created`, `user_23` and `users` reaches `Create`,
`FindOne` and `ListUser` respectively. Nothing else is needed on either side.

## Why handlers are `func(*gin.Context)`

A message handler has the same signature as an HTTP handler, and that is
deliberate: internally each handler is dispatched through a private Gin engine, so
everything you already use keeps working unchanged.

| What you write | Works on messages |
|---|---|
| `c.ShouldBindJSON(&dto)` | ✅ the payload is the request body |
| `validator.BindAndValidate(c, &dto)` | ✅ same validators, same error shape |
| `c.JSON(201, ...)` | ✅ becomes the reply payload |
| `guard:"Auth(admin)"` | ✅ the same guard registry as routes |
| `response.BadRequest(c, ...)` | ✅ becomes a structured envelope error |
| `c.Param("id")` | ❌ messages have no path — use `microservice.PatternFrom(c)` |

The alternative — synthesising a `*gin.Context` by hand — means reimplementing
Gin's semantics and drifting from them on every release. Dispatching through a
real engine means there is exactly one code path to reason about.

## Setting up a server

Setup takes a transport and its options. That is the whole configuration.

```go
package main

import (
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/redismq"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: redismq.MustNew(redismq.Options{
            URL: "redis://localhost:6379",
        }),
    })

    app.LoadModule(src.NewAppModule())

    // Listen starts the HTTP server *and* the message consumers, and drains both
    // on SIGINT/SIGTERM.
    app.Listen(":3001")
}
```

`Setup` does not begin consuming. Consumers start from `app.Listen` (or
`app.Start`), because a consumer that started during setup would dispatch its
first messages before `LoadModule` had registered the handlers that serve them.

Setup order does not otherwise matter: `Setup` may come before or after
`LoadModule` and will still see every controller.

### A worker with no HTTP listener

```go
app := nika.NewApp()
server, _ := microservice.Setup(app, microservice.Config{Transport: transport})
app.LoadModule(src.NewWorkerModule())

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

server.Listen(ctx) // blocks until the signal, then drains
```

### Serving several transports at once

```go
microservice.Setup(app, microservice.Config{
    Transports: []microservice.Listener{
        redismq.MustNew(redismq.Options{URL: "redis://localhost:6379"}),
        kafkamq.MustNew(kafkamq.Options{Brokers: []string{"localhost:9092"}, GroupID: "users"}),
    },
})
```

Each handler is routed by its `transport` tag matching a transport's name, so
`transport:"redis"` and `transport:"kafka"` handlers coexist in one controller.

## Sending messages

A client takes a transport and its options too, and from there every call is a
pattern plus a payload.

```go
client, _ := microservice.SetupClient(app, redismq.MustNew(redismq.Options{
    URL: "redis://localhost:6379",
}))
```

`SetupClient` registers `*microservice.Client` in the DI container, so a service
can inject it:

```go
func NewOrderService(client *microservice.Client) *OrderService {
    return &OrderService{client: client}
}
```

### Fire and forget

```go
err := client.Emit(ctx, "user_created", CreateUserDto{Name: "Ada"})
```

Returns as soon as the transport accepts the message. Delivery guarantees are
whatever the transport provides — see the per-transport pages.

### Request/reply

```go
var user User
err := client.Send(ctx, "user_23", nil, &user)
```

`Send` decodes a successful reply into `&user`. A reply carrying a handler error
comes back as an `*EnvelopeError`, which is what lets you tell *"the remote
service said no"* from *"the message never arrived"* — the distinction that
decides whether retrying is safe:

```go
var envErr *microservice.EnvelopeError
switch {
case errors.As(err, &envErr):
    // The service answered. envErr.Code is the handler's status; do not retry a 4xx.
case errors.Is(err, microservice.ErrTimeout):
    // Nobody answered in time. Retrying may be correct.
}
```

For the raw reply, use `client.Request`, which returns the `*Envelope` with its
`Status`, `Data` and `Error` untouched.

## Patterns

| Pattern | Matches |
|---|---|
| `user_created` | only `user_created` |
| `user_*` | `user_23`, `user_created`, `user_` |
| `user_?` | `user_1`, but not `user_23` |
| `*` | everything |

Wildcards are **character-level**, not token-level: `user_*` matches `user_23`
even though there is no separator. `*` matches any run of characters including
none, and `?` matches exactly one.

Wildcards only ever appear on the receiving side. Sending to a wildcard is
rejected, because it is ambiguous about which service should answer.

### Precedence

When several patterns match one subject, the most specific wins:

1. Exact patterns beat wildcards — so `user_created` reaches the `user_created`
   handler even though `user_*` also matches it.
2. Among wildcards, more literal characters beat fewer — `user_admin_*` beats
   `user_*`.
3. Ties break on fewer wildcards, then lexicographically.

The ordering is total, so **registration order never affects dispatch**.

### Reading the literal subject

A wildcard handler needs to know what it was actually asked for:

```go
FindOne: func(c *gin.Context) {
    id := strings.TrimPrefix(microservice.PatternFrom(c), "user_") // "23"
    ...
}
```

| Helper | Returns |
|---|---|
| `microservice.PatternFrom(c)` | the literal subject the client sent |
| `microservice.MessageFrom(c)` | the whole `*Envelope`, or `nil` over HTTP |
| `microservice.RouteFrom(c)` | the matched route, including its declared pattern |
| `microservice.IsMessage(c)` | whether this invocation is a message |

`IsMessage` is what lets one handler serve both entry points:

```go
Get: func(c *gin.Context) {
    id := c.Param("id")
    if microservice.IsMessage(c) {
        id = strings.TrimPrefix(microservice.PatternFrom(c), "user_")
    }
    ...
}
```

## Validation

Message payloads validate exactly like request bodies:

```go
Create: func(c *gin.Context) {
    var dto CreateUserDto
    if !validator.BindAndValidateMicroservice(c, &dto) {
        return // the 422 envelope is already written
    }
    ...
}
```

`BindAndValidateMicroservice` is the same function as `BindAndValidate` under a
name that reads correctly at a message call site. Both bind the payload and run
the validators you registered in `validator.Setup`, and both produce the
framework's `VALIDATION_ERROR` envelope — which the client then receives as an
`*EnvelopeError` with `Details` naming the failing fields.

## Guards

`guard` tags work on message handlers, using the same registry as routes:

```go
type AdminController struct {
    Delete func(*gin.Context) `transport:"nats" pattern:"user_delete" guard:"Auth Roles(admin)"`
}
```

The guard reads its credentials from the envelope headers, which the client sets:

```go
client := microservice.NewClient(transport,
    microservice.WithHeader("Authorization", "Bearer "+serviceToken))
```

Envelope headers come from a remote publisher, so the framework drops the ones
that could do harm: anything containing CR/LF (header smuggling), the body-framing
headers, and `X-Forwarded-For`/`X-Real-IP` (which would let a publisher spoof
`c.ClientIP()`). Application headers pass through untouched.

## The envelope

Every transport carries the same JSON envelope, which is what makes a handler
portable between them:

```json
{
  "id":      "9f86d081884c7d65...",
  "pattern": "user_created",
  "data":    { "name": "Ada" },
  "headers": { "Authorization": "Bearer ..." },
  "replyTo": "nika:reply:client-1",
  "sentAt":  "2026-07-24T10:00:00Z"
}
```

A reply adds `status` (the HTTP status the handler produced) and, on failure,
`error`:

```json
{
  "id":      "9f86d081884c7d65...",
  "pattern": "user_created",
  "status":  422,
  "error":   { "code": 422, "message": "VALIDATION_ERROR", "details": [...] }
}
```

`id` is 128 bits of `crypto/rand`. That matters on transports where replies share
a channel: a predictable id would let one client harvest another's reply.

Decoded envelopes are capped at 8 MiB — transports hand the framework bytes
straight off the network, and an unbounded payload from an untrusted publisher is
a trivial memory-exhaustion attack.

## Choosing a transport

| Transport | Delivery | Request/reply | Use it for |
|---|---|---|---|
| [Redis](redis.md) | at-most-once, no persistence | yes | events where a missed message is acceptable |
| [NATS](nats.md) | at-most-once (core) | native, fast | low-latency RPC and events |
| [RabbitMQ](rabbitmq.md) | at-least-once, durable | yes | work queues that must survive a restart |
| [Kafka](kafka.md) | at-least-once, replayable | possible, slow | event streams, audit logs, replay |
| [gRPC](grpc.md) | none — synchronous | native | service-to-service calls |
| [TCP](tcp.md) | none — synchronous | yes | sidecars, embedded, tests |
| `memory` | in-process | yes | tests, and a modular monolith |

The in-memory transport implements the full contract, so a modular monolith can
run today with `microservice.NewMemory()` and be split into real services later by
swapping one constructor.

## Operational behaviour

**Reconnection.** A transport that returns an error from `Listen` is restarted
with exponential backoff (250 ms doubling to 30 s). A broker restart does not
silently take a consumer offline for the lifetime of the process.

**Concurrency.** `Config.Concurrency` (default 64) caps how many messages are
handled at once per transport. Set it to `1` where per-partition ordering
matters — see the Kafka page.

**Timeouts.** `Config.HandlerTimeout` (default 30 s) bounds one handler
invocation. Without it a single stuck handler holds a concurrency slot forever
and the consumer eventually wedges.

**Panics.** Install `nika.RecoveryMiddleware()` in `Config.Middleware`. The
message engine is separate from the HTTP engine, so the app's recovery does not
cover it — and an escaping panic in a consumer stops *every* subject, not just
the failing one:

```go
microservice.Setup(app, microservice.Config{
    Transport:  transport,
    Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
})
```

**Shutdown.** `Setup` registers a shutdown hook, so `app.Listen` drains
in-flight handlers and closes the broker connections on SIGTERM.

**Unrouted subjects** are answered with a `404` / `PATTERN_NOT_FOUND` envelope
rather than dropped, so a caller learns its pattern is wrong instead of timing
out.

## Testing

Message handlers are tested with no broker running:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")

ms.ExpectRoutesTo("user_23", "user_*")
```

See [Testing](../fundamentals/testing.md).
