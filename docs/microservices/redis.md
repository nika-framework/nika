# Redis Transporter

The Redis transport carries messages over Redis pub/sub: one channel per pattern,
namespaced by a prefix, with literal patterns `SUBSCRIBE`d and wildcard patterns
`PSUBSCRIBE`d so the broker does the filtering. The single most important thing to
know is that Redis pub/sub stores nothing, so a consumer that is not connected at
the instant of publication never receives the message.

See [Microservices Overview](basics.md) for patterns, handlers and the envelope.

## Delivery guarantees

Redis pub/sub is **at-most-once and has no persistence**. A message is delivered to
the subscribers connected at the moment it is published, and then forgotten. A
consumer that is restarting, deploying, GC-paused past its client buffer or briefly
disconnected does not get the message later — it never gets it.

!!! warning "A successful `Publish` does not mean anyone received the message"
    `PUBLISH` returns the number of subscribers that received the message, and zero
    is a success at the protocol level. Nothing about a nil error from `Emit`
    implies delivery. There is no acknowledgement, no retry and no queue.

That makes this transport a good fit for cache invalidation, presence, live
dashboards and any traffic where the next message supersedes the last. It is a poor
fit for anything a business depends on. When delivery has to survive a consumer
restart, use a log with consumer groups — Redis Streams (`XADD`/`XREADGROUP`) or a
broker-backed transport such as [RabbitMQ](rabbitmq.md) or [Kafka](kafka.md), where
unacknowledged messages are still there afterwards.

Request/reply works, and it works over the same pub/sub primitives: the reply travels
on a private inbox channel whose name carries a random 128-bit id, so another client
cannot subscribe to it by guessing. If the responder is down, `Send` returns
`microservice.ErrTimeout`.

## Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/redismq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: redismq.MustNew(redismq.Options{
            URL: "redis://localhost:6379",
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"redis"`:

```go
type UserController struct {
    Create  func(*gin.Context) `transport:"redis" pattern:"user_created"`
    FindOne func(*gin.Context) `transport:"redis" pattern:"user_*"`
}
```

A client, handling the construction error rather than panicking:

```go
transport, err := redismq.New(redismq.Options{
    URL:         "redis://localhost:6379",
    Prefix:      "orders",
    HealthCheck: true, // PING on construction; fail fast if Redis is unreachable
})
if err != nil {
    return fmt.Errorf("redis transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

`MustNew` is the one-liner for wiring code where a bad option is a programming
error; `New` is for anywhere you want to return the error.

## Options

| Field | Default | Purpose |
|---|---|---|
| `URL` | — | Redis connection string (`redis://:pass@host:6379/0`, `rediss://…` for TLS). **Required unless `Client` is set.** |
| `Client` | `nil` | Reuse an existing `*redis.Client`. `Close` does not close a client it did not create. |
| `Prefix` | `"nika"` (`DefaultPrefix`) | Namespaces every channel as `prefix + ":" + pattern`. |
| `Concurrency` | `64` | Caps messages dispatched at once by this transport. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request/reply deadline when the caller passes none. |
| `PingTimeout` | `2s` | Bounds the health check. |
| `HealthCheck` | `false` | Make `New` verify connectivity with a `PING` and fail if the server is unreachable. |
| `Logger` | `slog.Default()` | Receives decode failures and subscription errors. |

Setting both `URL` and `Client` is an error, as is a negative `Concurrency`.

`HealthCheck` is off by default because a transport is normally constructed while
wiring the application, when Redis may legitimately not be up yet. Turn it on when
you would rather the process refuse to start. `Ping(ctx)` is exported so you can
check on your own schedule — it is what belongs in a readiness probe.

## The prefix is your only isolation

Redis pub/sub has no vhosts, and `PUBLISH` is **not affected by `SELECT`** — the
selected database does not scope it. `Prefix` is therefore the only thing keeping
two unrelated services on one Redis instance from receiving each other's traffic.
Give each logical system its own prefix, and do not assume `redis://…/15` isolates
anything.

The prefix is treated as a literal namespace: every glob metacharacter in it is
escaped before subscribing, so a stray `*` in a prefix cannot silently turn a
subscription into a firehose over every other service on the instance.

## Patterns are escaped before they reach Redis

Nika patterns and Redis globs overlap but are not the same language. Both use `*`
for "any run of characters" and `?` for "exactly one character" — which is why
`PSUBSCRIBE` can do the filtering at all. But Redis additionally treats `[`…`]` as
a character class and `\` as an escape, while a Nika pattern treats all three as
ordinary characters.

Handing an untranslated pattern to `PSUBSCRIBE` would silently change its meaning:
`item[1]` would stop matching the literal subject `item[1]` and start matching
`item1`. So `[`, `]` and `\` are escaped on the way to the broker and `*`/`?` pass
through, which makes a pattern mean exactly the same thing at the broker as it does
in `microservice.Pattern.Match`.

`reply:` is a reserved namespace — it is where request/reply inboxes live — so a
handler pattern may not start with it. That is what stops a handler subscription
from straddling another client's reply inbox.

## Overlapping subscriptions are collapsed for you

Redis delivers a message once per *matching subscription*. A service registering
both `user_created` and `user_*` would get the literal message twice: once as a
`message` from `SUBSCRIBE` and once as a `pmessage` from `PSUBSCRIBE`. Two
overlapping globs produce two `pmessage`s.

The transport assigns each channel exactly one owning subscription and accepts only
that delivery, so a handler runs once. The exact channel wins when there is one,
mirroring the Router's "exact beats wildcard" rule; otherwise the first matching
glob wins, and because patterns arrive in specificity order that is the same glob
whose handler the Router would pick. You do not have to make your handlers
idempotent to compensate for a doubled delivery — but on an at-most-once transport
they should be idempotent anyway, because a retrying publisher is your only
recovery mechanism.

## A saturated consumer loses messages

`Concurrency` bounds how many messages this transport dispatches at once. Reaching
the cap pauses reads from the subscription channel, which go-redis buffers — and if
that buffer stays full for a minute, **go-redis drops messages**. A permanently
saturated consumer loses traffic rather than queueing it.

This is the same failure as everywhere else on this transport: there is no
backpressure toward the publisher, so the only outcomes are "keep up" and "lose
messages". Size `Concurrency` for your throughput, watch for the drop, and if you
find yourself tuning around it you have outgrown pub/sub.

An undecodable payload is logged and skipped rather than fatal, so one publisher
writing garbage — a different service on the same prefix, a stray `redis-cli
PUBLISH` — cannot tear down a subscription serving everyone else.

## Testing

Handlers do not need Redis. `nikatest.NewMicroservice(t)` runs the whole message
stack over the in-memory transport:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")
```

See [Testing](../fundamentals/testing.md).

The transport's own integration suite needs a real server and is excluded from a
default `go test ./...` by a build tag:

```bash
REDISMQ_TEST_URL=redis://localhost:6379/15 \
  go test -tags redis_integration -race ./common/microservice/transport/redismq/
```

Use a throwaway database. The tests publish on a random prefix so they do not
collide with each other, but pub/sub is instance-wide and a noisy instance makes
them flaky.

## When to use it, and when not to

Use Redis when you already run Redis, the traffic is events rather than commands,
and a missed message is acceptable because the next one supersedes it: cache
invalidation, presence, live dashboards, config reload fan-out. Broadcast is the
natural shape — every subscriber gets every matching message, so scaling out
multiplies the work rather than dividing it.

Do not use it when a message must survive a consumer restart, when work needs to be
load-balanced across replicas, or when a lost message would need to be reconciled by
hand. [RabbitMQ](rabbitmq.md) gives you at-least-once delivery with a durable queue
and per-replica work sharing; [Kafka](kafka.md) gives you a replayable log;
[NATS](nats.md) gives you the same at-most-once model as Redis but with native
request/reply, queue groups for load balancing, and JetStream when you need
durability.
