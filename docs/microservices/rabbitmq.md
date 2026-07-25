# RabbitMQ Transporter

The RabbitMQ transport speaks AMQP 0-9-1 over a topic exchange. Publishers address a
subject, the broker decides which queues want it, and adding a consumer needs no
change on the publisher. Queues are the unit of work distribution: every replica of a
service consumes the same queue, so the broker load-balances between them, while two
different services use two different queues and both see the message. The one thing
to get right before anything else is the queue name — the default is a durable, named,
shared queue for a reason.

See [Microservices Overview](basics.md) for patterns, handlers and the envelope.

## Delivery guarantees

This is the durable transport of the set. Three defaults produce **at-least-once**
delivery, and each is worth understanding:

- **Publisher confirms are on.** A bare AMQP publish is fire-and-forget even *towards
  the broker*: it is a one-way frame, so a message the broker rejected or dropped
  looks exactly like one it accepted. With confirms, `Publish` waits for the broker's
  verdict and returns an error when the broker nacks — a full disk, a failed quorum
  write. Set `DisableConfirms` to trade that for throughput.
- **Acknowledgement is manual.** A message is acked *after* the handler succeeds, so a
  consumer crash mid-handler redelivers rather than loses. `AutoAck` acknowledges on
  delivery instead, which makes the transport at-most-once — the broker forgets the
  message as soon as it hits the socket.
- **A failed handler nacks without requeue.** A message that fails deterministically
  therefore cannot spin the consumer in a hot loop. Configure `DeadLetterExchange` to
  keep those messages instead of discarding them.

A publisher confirm proves the broker *accepted* a message. It never proves anybody
was bound to receive it. Set `Mandatory` to have the broker return unroutable
messages; returns are logged rather than surfaced from `Publish`, because an
unroutable message is both Returned *and* Acked and there is no single verdict to
report.

Handlers should still be idempotent. At-least-once means exactly that: a redelivery
after a crash between the handler's database write and its ack is normal operation,
not a bug.

## Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/rabbitmq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: rabbitmq.MustNew(rabbitmq.Options{
            URL:                "amqp://guest:guest@localhost:5672/",
            Queue:              "users.workers", // distinct per service
            DeadLetterExchange: "nika.dlx",
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"rabbitmq"`:

```go
type UserController struct {
    Create  func(*gin.Context) `transport:"rabbitmq" pattern:"user_created"`
    FindOne func(*gin.Context) `transport:"rabbitmq" pattern:"user_find"`
}
```

A client, handling the error:

```go
transport, err := rabbitmq.New(rabbitmq.Options{
    URL:   "amqp://guest:guest@localhost:5672/",
    Queue: "orders.workers",
})
if err != nil {
    return fmt.Errorf("rabbitmq transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

`New` does not dial. Connections are established lazily and re-established after a
drop, so constructing a transport never fails because the broker happens to be
restarting, and a wiring mistake surfaces as a configuration error rather than as a
timeout. A dial failure is logged with the credentials stripped from the URL.

## Options

| Field | Default | Purpose |
|---|---|---|
| `URL` | — | AMQP dial string, e.g. `amqp://guest:guest@localhost:5672/`. **Required unless `Conn` is set.** Ignored when `Conn` is set. |
| `Conn` | `nil` | Reuse an existing `*amqp.Connection`. `Close` closes this transport's channels but never a connection it did not dial. |
| `Exchange` | `"nika"` (`DefaultExchange`) | The topic exchange to publish to and bind against. |
| `ExchangeType` | `"topic"` (`DefaultExchangeType`) | Anything else disables broker-side pattern routing and leaves all filtering to the Router. |
| `Queue` | `"nika.workers"` (`DefaultQueue`) | This service's queue. **Give every service a distinct name** — see below. |
| `Durable` | `Bool(true)` | Declare a topology that survives a broker restart. A `*bool`, because the safe value is not the zero value; use `rabbitmq.Bool(false)` to opt out. |
| `AutoDelete` | `false` | Delete the queue when its last consumer disconnects. Leave it false — see below. |
| `QueueArgs` | `nil` | Extra queue x-arguments (`x-max-length`, `x-queue-type`, `x-message-ttl`, …). `DeadLetterExchange` is merged into these. |
| `DeadLetterExchange` | `""` | Declares the queue with `x-dead-letter-exchange`, so a nacked message is republished there instead of discarded. |
| `Prefetch` | `32` (`DefaultPrefetch`) | QoS prefetch count, applied per consumer. |
| `Concurrency` | `Prefetch` | Deliveries handled at once by this transport. |
| `AutoAck` | `false` | Acknowledge on delivery instead of after the handler. Makes delivery at-most-once. |
| `Requeue` | `false` | Make a failed handler nack with `requeue=true`. See the poison-message section. |
| `Mandatory` | `false` | Ask the broker to return a message that matched no queue. Returns are logged. |
| `DisableConfirms` | `false` | Turn off publisher confirms, so `Publish` returns as soon as the frame is written. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request deadline when the caller passes none. |
| `TLSConfig` | `nil` | Used when `URL` has the `amqps` scheme. |
| `Heartbeat` | `10s` (`DefaultHeartbeat`) | AMQP heartbeat interval. AMQP over a NAT or a load balancer dies silently without one. |
| `Logger` | `slog.Default()` | Receives malformed-message and reconnect events. |

`Concurrency` defaults to `Prefetch` because a prefetch larger than the concurrency
just parks messages in a client-side buffer, where they are invisible to the broker's
queue-depth metrics.

## The queue name is the expensive mistake

!!! warning "An anonymous auto-delete queue silently drops everything published during a deploy"
    An anonymous, auto-delete queue exists only while its consumer is connected. Every
    message published during a deploy, a crash loop or a broker reconnect is dropped by
    the broker with **no error anywhere**: the publisher's confirm succeeds, the exchange
    matches no queue, and the message is gone. A durable named queue keeps accumulating
    while the service is down and drains when it returns.

That is why `Queue` defaults to `"nika.workers"` — durable, named, shared — rather
than to a server-generated name, and why `Durable` is `Bool(true)` rather than a plain
`bool` whose zero value would be `false`. Leave `AutoDelete` alone unless the queue
really is disposable.

Two more consequences of "the queue is the unit of distribution":

- **Every service sharing an `Exchange` must set a distinct `Queue`.** Two services on
  one queue *compete* for messages instead of both receiving them, so roughly half of
  each service's messages go to the other one.
- **Every replica of one service shares its queue.** That is what makes scaling out
  divide the work instead of multiplying it — the opposite of the broadcast default on
  [NATS](nats.md) without a queue group.

## Wildcard patterns force a `#` binding

An AMQP topic binding key looks like a Nika pattern and is not. AMQP topic keys are
dot-separated **words**, and its metacharacters occupy a whole word: `*` matches
exactly one word, `#` matches zero or more words. A word that merely *contains* a
metacharacter — `user_*` — is not a wildcard at all; RabbitMQ compares it literally,
so binding `user_*` receives nothing except a message literally routed to `user_*`.

Nika wildcards are character-level: `user_*` matches `user_23` and `user_created`.
There is therefore no AMQP binding key that expresses "any suffix within one word",
which is the common case in this framework. The resolution:

- A **literal pattern** binds to its own routing key, so the broker filters and the
  queue only sees traffic it can handle.
- **Any wildcard pattern** forces one catch-all `#` binding, and the core Router does
  the character-level filtering in-process.

The cost is real and worth stating: the queue then receives **every message published
to the exchange**, including subjects owned by other services sharing it, which the
Router answers with a `404` / `PATTERN_NOT_FOUND` envelope. A warning is logged when
this happens. Give each service its own `Exchange` — or its own vhost — when that
matters.

When the catch-all is required, the literal binding keys are dropped from the plan.
That is an efficiency choice, not a correctness requirement: RabbitMQ guarantees that
for every queue a message is routed to, the queue receives exactly one copy of that
message, so keeping both a `#` and a literal binding on the same queue would **not**
duplicate deliveries. `#` simply already subsumes the literals, which makes the extra
bindings pure bookkeeping in the broker's binding table.

### Patterns AMQP cannot carry

These are rejected before the transport touches the network, since a pattern AMQP
cannot express is a programming error rather than a broker problem:

| Rejected | Why |
|---|---|
| `.` in a pattern | The AMQP word separator. `user.created` would be two words and would start matching `*` bindings the author never intended. Use `_`. |
| `#` in a pattern | The AMQP multi-word wildcard. Allowing it would let a pattern quietly subscribe to the whole exchange. |
| whitespace or a control character | Not usable in a key. |
| over 255 bytes | The AMQP short-string limit for a routing or binding key. |

Publishing to a wildcard subject is rejected too — a published subject must be
literal.

## `Prefetch` is what balances work across replicas

Without a QoS prefetch the broker pushes the whole queue at whichever consumer
connected first, and every other replica idles with an empty queue while the first one
drowns. `Prefetch` defaults to 32 and is applied *per consumer* (`global=false`), which
is what makes the balance work — a connection-wide budget would not.

## Poison messages

A handler failure nacks **without** requeue by default. That is deliberate: a requeued
message goes back to the head of the queue and is redelivered immediately, so a
message that fails deterministically — a malformed payload, a bug, a missing row — is
redelivered forever at the speed of the CPU. That is the classic AMQP poison-message
outage.

The supported way to keep those messages is a dead-letter exchange:

```go
rabbitmq.MustNew(rabbitmq.Options{
    URL:                "amqp://guest:guest@localhost:5672/",
    Queue:              "users.workers",
    DeadLetterExchange: "nika.dlx",
})
```

A nacked message is then republished there instead of discarded, and an operator can
inspect and replay it without the consumer ever retrying in a loop.

!!! note "Changing `DeadLetterExchange` on an existing queue"
    AMQP queue arguments are immutable. Changing this value requires deleting and
    redeclaring the queue; a mismatched redeclaration fails the channel with
    `PRECONDITION_FAILED`.

Turn `Requeue` on only when your handler failures are genuinely transient, and pair it
with `DeadLetterExchange` plus a queue `x-delivery-limit` so the broker itself caps the
retries. A retry counter derived from the `x-death` header is deliberately not
implemented: `x-death` only exists *after* the broker has already dead-lettered the
message, so counting it in the consumer reimplements broker policy in application code
and silently stops working when an operator changes the dead-letter topology. Retry
limits belong in the queue definition.

A message whose bytes do not decode is rejected without requeue whatever `Requeue`
says, because bytes that do not parse now will not parse on redelivery either — that
is a guaranteed infinite loop, not a retry.

## Request/reply

`Request` uses the standard AMQP RPC pattern: one long-lived exclusive reply queue per
client process, correlation by `CorrelationId`, and a single consumer demultiplexing
into a map of waiters. The reply queue is established — declared and consumed —
*before* anything is published, so a peer that answers instantly cannot beat the
client to its own mailbox.

The request carries an `Expiration` equal to its timeout, so the broker drops it once
the caller's deadline has passed. A backed-up queue then does not hand a consumer work
nobody is listening for any more.

A lost connection while waiting fails the request at once rather than after the full
timeout, because a reply can no longer arrive over a dead channel.

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

The routing translation — the part of an AMQP integration most likely to be wrong, and
the hardest to observe at runtime, since a bad binding fails by receiving nothing
rather than by erroring — is unit-tested without a broker. The rest of the suite needs
a live broker and is behind a build tag:

```bash
RABBITMQ_URL=amqp://guest:guest@localhost:5672/ \
  go test -race -tags rabbitmq_integration ./common/microservice/transport/rabbitmq/
```

## When to use it, and when not to

Use RabbitMQ when messages are work that must not be lost: a queue that keeps
accumulating while a consumer is down, per-message acknowledgement, per-replica work
sharing, and a dead-letter exchange for the ones that will never succeed. It is the
right default for command-style traffic — "charge this card", "send this email",
"resize this image" — where a dropped message needs a human to notice.

Do not use it as an event log. A message is gone once acked, so there is no replay and
no second consumer group arriving later to read history — that is
[Kafka](kafka.md). Do not reach for it for latency-sensitive RPC either: it works, but
each call costs a broker hop in each direction, where [NATS](nats.md) and
[gRPC](grpc.md) are direct. And if a missed message is genuinely acceptable,
[Redis](redis.md) or NATS will do the same job with less operational surface.
