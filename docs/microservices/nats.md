# NATS Transporter

NATS is the best-matched broker for this layer: it has native request/reply with
server-side correlation, so this transport keeps no correlation map at all, and it
reports "no responders" immediately instead of making a caller wait out a timeout to
discover that nobody is listening. What it does not have is character-level
wildcards — NATS subjects are dot-separated tokens and its wildcards work on whole
tokens — and that gap is the one thing to understand before you use it.

See [Microservices Overview](basics.md) for patterns, handlers and the envelope.

## Delivery guarantees

Core NATS is **at-most-once**. A subscriber that is down misses messages, exactly as
with [Redis](redis.md) pub/sub. A `Publish` is asynchronous and buffered, so a nil
error means the message was queued to the connection, not that a subscriber received
it. Call `Ping` — which flushes — when a publish must be known to have reached the
server.

NATS JetStream adds persistence and acknowledgement and is the right answer when a
message must survive a consumer restart. This transport speaks core NATS.

Request/reply is where NATS is genuinely better than the alternatives. It is native:
the client library allocates a per-connection inbox, tags the request with a unique
reply subject and matches the response, so there is no correlation map to leak and no
extra identifier to keep consistent. And an unroutable request comes back
**immediately** as `microservice.ErrNoHandler` rather than as a timeout:

```go
var user User
err := client.Send(ctx, "user_find", FindDto{ID: 23}, &user)

switch {
case errors.Is(err, microservice.ErrNoHandler):
    // Nobody is subscribed. Reported at once, not after 10 seconds.
case errors.Is(err, microservice.ErrTimeout):
    // Somebody is subscribed but did not answer in time.
}
```

That distinction is worth a lot during an incident: "the service is not deployed" and
"the service is slow" stop looking the same.

## Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/natsmq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: natsmq.MustNew(natsmq.Options{
            URL:  "nats://localhost:4222",
            Name: "users", // identifies the connection, and seeds QueueGroup
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"nats"`:

```go
type UserController struct {
    Create  func(*gin.Context) `transport:"nats" pattern:"user_created"`
    FindOne func(*gin.Context) `transport:"nats" pattern:"user_find"`
}
```

A client, handling the error:

```go
transport, err := natsmq.New(natsmq.Options{
    URL:       "nats://localhost:4222",
    Name:      "orders",
    CredsFile: "/etc/nats/orders.creds",
})
if err != nil {
    return fmt.Errorf("nats transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

`New` connects eagerly, because NATS reports authentication failures, TLS problems
and unreachable servers on connect and those are startup problems rather than
per-message problems. Set `LazyConnect` when the process must be able to start before
the broker is reachable.

## Options

| Field | Default | Purpose |
|---|---|---|
| `URL` | `nats.DefaultURL` | Server URL, or a comma-separated list for a cluster. |
| `Conn` | `nil` | Reuse an existing `*nats.Conn`. `Close` neither drains nor closes a connection it did not create. |
| `Prefix` | `"nika"` (`DefaultPrefix`) | Namespaces every subject as `prefix + "." + pattern`. |
| `QueueGroup` | `Name`, else empty | Load-balance across replicas. See below — this is the consequential setting. |
| `Concurrency` | `64` | Caps messages dispatched at once by this transport. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request/reply deadline when the caller passes none. |
| `Name` | `""` | Names the connection in `nats server report connections`. Also seeds `QueueGroup`. |
| `Token` | `""` | Bearer-token authentication. |
| `User`, `Password` | `""` | Credential authentication. |
| `NKeySeed` | `""` | Raw nkey seed (`SU…`). The ed25519 private key never leaves the process; the server is convinced by a signature over a per-connection nonce. |
| `CredsFile` | `""` | Path to a NATS JWT credentials file — the usual choice with a managed or multi-tenant deployment. |
| `TLSConfig` | `nil` | For mTLS or a private CA. A `tls://` URL alone does not pin anything. |
| `ReconnectWait` | `2s` | Pause between reconnect attempts. Reconnect attempts are unlimited. |
| `DrainTimeout` | `30s` | Bounds a graceful shutdown. |
| `LazyConnect` | `false` | Defer dialing to the first `Listen`, `Publish` or `Request` instead of connecting in `New`. |
| `Logger` | `slog.Default()` | Receives connection lifecycle and decode events. |

Setting both `URL` and `Conn` is an error, as is a negative `Concurrency` or a prefix
containing a character NATS reserves.

Reconnection is configured for an incident rather than for a demo: reconnect attempts
are unlimited, because the library default gives up after a handful of attempts and
then closes the connection permanently, turning a broker restart into a service that
stays broken until someone redeploys it. Disconnects, reconnects and slow-consumer
errors are all logged, because a silent reconnect loop is indistinguishable from a
healthy connection.

## `QueueGroup` decides how your service scales

This is the setting to get right, because both behaviours are silent:

| `QueueGroup` | Behaviour |
|---|---|
| set | Replicas join a queue group and NATS delivers each message to **exactly one** of them. This is a load-balanced service; scaling out adds throughput. |
| empty | **Every replica receives every message.** This is a broadcast; scaling out multiplies the work, and a non-idempotent handler now runs once per replica. |

Because the broadcast failure mode is silent and expensive — duplicate writes,
duplicate emails, duplicate charges, all proportional to your replica count — the
default leans toward safety: when `QueueGroup` is empty and `Name` is set, `Name` is
adopted as the queue group. A named service with several replicas almost always wants
load balancing.

To force broadcast anyway, ask for it explicitly:

```go
natsmq.MustNew(natsmq.Options{
    URL:        "nats://localhost:4222",
    Name:       "cache",
    QueueGroup: natsmq.NoQueueGroup, // every replica handles every message
})
```

`Transport.QueueGroup()` returns the effective value after this defaulting, so you can
log it and be sure.

## Wildcard patterns force a catch-all subscription

NATS wildcards are token based: `*` matches exactly one dot-separated token and `>`
matches all remaining tokens. Nika pattern wildcards are character based — `user_*`
matches `user_23` and `user_created`.

So `user_*` **is not a NATS wildcard at all**. `user_created` is one single NATS
token, and a subscription to `prefix.user_*` matches only the literal subject with
that one-token suffix. There is no NATS subject that expresses "any suffix within one
token".

The resolution:

- **Literal patterns** map one-to-one onto exact subjects, and the broker filters.
  This is the efficient path.
- **Any wildcard pattern** makes the process subscribe once to `prefix.>` and let the
  core Router do the character-level matching locally.
- When the catch-all is used, the literal subjects are deliberately **not** also
  subscribed. `>` already covers them, and NATS delivers a message once per matching
  subscription, so binding both would run every literal message's handler twice.

The cost of the catch-all is real: the process then receives **every message
published under the prefix**, including subjects owned by other services sharing it,
and discards most of them — after they have crossed this process's socket, JSON
decoder and CPU. The Router answers the unowned ones with a `404` /
`PATTERN_NOT_FOUND` envelope. A warning is logged when the catch-all is used, so this
is not something you have to discover from a flame graph.

The practical advice is to prefer literal patterns on this transport. If you need a
wildcard, give the service its own `Prefix` so the catch-all only sees its own
traffic.

## Patterns must fit in one subject token

A pattern is rejected at `Listen` time if it contains a character NATS reserves:

| Character | Why |
|---|---|
| `.` | The token separator — the pattern would silently become two tokens and change what the broker matches. |
| `>` | The NATS multi-token wildcard; a literal one would turn an exact subscription into a firehose. |
| space, tab | These terminate a subject in the NATS protocol's line format. |

Failing at `Listen` is the point. The alternative is a subscription the server
happily accepts and that never matches anything, which presents as "my handler is
never called" with nothing in any log. Use `_` where you would reach for `.`.

Publishing to a wildcard pattern is rejected too — a published subject must be
literal.

## Shutdown drains rather than drops

`Close` waits for in-flight handlers so their replies are still publishable, then
*drains* a connection it owns rather than hard-closing it: drain unsubscribes, lets
already-delivered messages finish, flushes anything still buffered, and only then
closes. A plain close would discard in-flight work, which on a request/reply subject
means every caller currently waiting gets a timeout instead of an answer.
`DrainTimeout` bounds the whole thing so a broker that will not let go cannot hold
shutdown hostage. A borrowed connection (`Options.Conn`) is left alone.

Subscriptions are drained on the way down too, so a shutdown does not throw away
messages the broker already considers delivered.

## A saturated consumer loses messages

`Concurrency` bounds concurrent dispatch. Reaching the cap stops draining the
subscription, and NATS enforces its own pending limits on an unread subscription: a
consumer that stays saturated is reported as a **slow consumer** and its messages are
**dropped**, not queued. The per-subscription pending limits are raised explicitly so
the limit is a deliberate choice rather than an accident, and slow-consumer errors are
logged at error level because they mean data loss.

## Testing

Handlers need no broker. `nikatest.NewMicroservice(t)` runs the whole message stack
over the in-memory transport:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")
```

See [Testing](../fundamentals/testing.md).

The transport's own integration suite needs a real server and is behind a build tag:

```bash
NATSMQ_TEST_URL=nats://localhost:4222 \
  go test -tags nats_integration -race ./common/microservice/transport/natsmq/
```

The tests use a random prefix per run, because the NATS subject space is flat and
account-wide and the prefix is the only isolation available.

## When to use it, and when not to

Use NATS for low-latency request/reply between services and for events where a missed
message is acceptable. It has the best RPC story of the brokers here — native
correlation, immediate "no responders", a flat and cheap subject space — plus queue
groups, which give you load-balanced replicas with one option instead of a topology.
nkey and JWT credentials make it comfortable in a multi-tenant deployment.

Do not use it when a message must survive the consumer being down: core NATS is
at-most-once, and this transport does not speak JetStream. Reach for
[RabbitMQ](rabbitmq.md) when you want a durable work queue with per-message
acknowledgement, or [Kafka](kafka.md) when you want a replayable log. Prefer another
transport if your patterns are heavily wildcarded, since every wildcard here costs a
catch-all subscription — or [gRPC](grpc.md) if the calls are strictly synchronous and
you would rather have mTLS and no broker at all.
