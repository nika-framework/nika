# Kafka Transporter

The Kafka transport carries every message on a **single topic**, with the subject in
the envelope and in a Kafka header, and sets the message key to the pattern so all
messages for one subject land on one partition and stay in order. It is built on
`segmentio/kafka-go`, and several of its defaults deliberately differ from kafka-go's
own — in each case because kafka-go's default can lose data quietly.

See [Microservices Overview](basics.md) for patterns, handlers and the envelope.

## Delivery guarantees

Producing waits for the full in-sync replica set (`kafka.RequireAll`). Consuming is
**at-least-once**: fetch, dispatch, then commit, so a crash before the commit
redelivers. And because Kafka retains the log, a message is **replayable** — a
consumer that is down when it is published still receives it when it comes back, which
is the opposite of [gRPC](grpc.md) and the reason to put events here.

!!! warning "kafka-go defaults to `RequireNone`, which loses writes on leader failover"
    `RequireNone` returns success as soon as the bytes are written and loses the write
    outright if the partition leader fails before replicating. This transport defaults
    to `RequireAll` instead. `RequireNone` cannot even be selected here: it is the zero
    value of `kafka.RequiredAcks` and is therefore treated as "unset". `RequireOne`
    acknowledges from the leader alone, so an unreplicated write is lost when that
    leader fails over. Use kafka-go directly if you truly need either.

Two related defaults:

- **`StartOffset` is `kafka.LastOffset`.** kafka-go defaults to `FirstOffset`, which
  replays the entire retention window — days or weeks of events — the first time a new
  consumer group starts. That is occasionally what you want and never what you expect.
  Set `kafka.FirstOffset` deliberately when you mean to replay.
- **`Async` is off.** With `Async` the write error is delivered to nobody and `Publish`
  cannot fail, which quietly turns `RequiredAcks` into a decoration.

`CommitInterval` is the knob that trades the guarantee away. Zero — the default —
commits synchronously after the handler returns: at-least-once. Greater than zero
commits in the background on a timer, so commits race the handlers, an offset can be
committed for a message that has not been handled, and a crash loses it: at-most-once.
It buys throughput on a high-volume topic where a dropped message is acceptable. It is
a real trade-off, not a tuning knob.

## Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/kafkamq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: kafkamq.MustNew(kafkamq.Options{
            Brokers: []string{"localhost:9092"},
            Topic:   "nika",
            GroupID: "users", // required to Listen
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"kafka"`:

```go
type UserController struct {
    Created func(*gin.Context) `transport:"kafka" pattern:"user_created"`
    Updated func(*gin.Context) `transport:"kafka" pattern:"user_updated"`
}
```

A publish-only client needs no `GroupID`:

```go
transport, err := kafkamq.New(kafkamq.Options{
    Brokers: []string{"localhost:9092"},
    Topic:   "nika",
})
if err != nil {
    return fmt.Errorf("kafka transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

`New` makes no connection: kafka-go's reader and writer dial lazily, so a broker that
is briefly unavailable does not stop the process from starting.

## Options

| Field | Default | Purpose |
|---|---|---|
| `Brokers` | — | Bootstrap broker list. **Required.** |
| `Topic` | `"nika"` (`DefaultTopic`) | Carries every message. |
| `GroupID` | `""` | Consumer group. **Required by `Listen`** — see below. |
| `ReplyTopic` | `""` | Where replies to `Request` are produced and consumed. `Request` returns `microservice.ErrNotSupported` while it is empty. |
| `Partition` | `0` | Pins a reader to one partition. Mutually exclusive with `GroupID`; only useful for a single-consumer tool or a test. |
| `MinBytes` | `1` (`DefaultMinBytes`) | Lets the broker answer a fetch as soon as it has anything, which keeps latency low on a quiet topic. |
| `MaxBytes` | `8 MiB` (`DefaultMaxBytes`) | Bounds one fetch response. Must exceed the largest envelope, or the broker truncates and the consumer stalls. Matches the framework's envelope cap. |
| `MaxWait` | `1s` (`DefaultMaxWait`) | How long a fetch waits for `MinBytes`. kafka-go's own 10s makes shutdown feel hung. |
| `StartOffset` | `kafka.LastOffset` | Where a group begins with no committed offset. Only `kafka.FirstOffset` and `kafka.LastOffset` are accepted. |
| `Concurrency` | `1` (`DefaultConcurrency`) | Handlers running at once per `Listen`. Above 1 gives up ordering — see below. |
| `CommitInterval` | `0` | `0` commits synchronously after the handler (at-least-once); above zero commits on a timer (at-most-once). |
| `Balancer` | `&kafka.Hash{}` | Chooses the partition. `Hash` is what makes the key (the pattern) map to a stable partition; `RoundRobin` or `LeastBytes` gives up per-subject ordering. |
| `RequiredAcks` | `kafka.RequireAll` | Replicas that must acknowledge a produce. |
| `Async` | `false` | Return before the broker answers. Makes `Publish` unable to fail. |
| `Dialer` | `nil` | Reader dialer. Built from `DialTimeout`/`TLS`/`SASL` when nil. |
| `Transport` | `nil` | Writer `kafka.RoundTripper`. Built from `TLS`/`SASL` when nil. |
| `TLS`, `SASL` | `nil` | Authentication for the generated dialer and transport. Ignored when `Dialer`/`Transport` are supplied. |
| `DialTimeout` | `10s` (`DefaultDialTimeout`) | Bounds a connection attempt. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request deadline when the caller passes none. |
| `CreateTopics` | `false` | Create `Topic` and `ReplyTopic` if missing. Off by default — see below. |
| `Partitions` | `1` (`DefaultPartitions`) | Used only by `CreateTopics`. |
| `ReplicationFactor` | `1` (`DefaultReplicationFactor`) | Used only by `CreateTopics`. |
| `Logger` | `slog.Default()` | Receives malformed-message and lifecycle events. |

`MaxBytes` below `MinBytes`, a negative `CommitInterval`, a `StartOffset` that is
neither sentinel, and setting both `Partition` and `GroupID` are all rejected by `New`.

## One topic, not a topic per pattern

A topic per pattern is the obvious first design and the wrong one:

- **Kafka's unit of parallelism is the partition, not the topic.** A thousand patterns
  become a thousand topics and at least a thousand partitions, each with its own
  replicas, index files, open file handles and controller metadata. Partition count is
  the number that actually limits a Kafka cluster.
- **Topics have no wildcard subscriptions.** A regex consumer exists, but it matches
  topic *names* and rebalances the whole group whenever a topic appears — exactly what
  a dynamic pattern space would do.
- **Ordering is per partition.** Splitting one logical stream across topics forfeits any
  ordering guarantee between them.

So there is one topic, the pattern travels in the envelope (and in the `nika-pattern`
header, for tooling that must not parse payloads), and the core Router does the
matching in-process. `patterns` are not used for broker-side filtering — there is
nothing to filter at — but they are logged so a misconfiguration is visible.

## `Key` is the pattern, which buys ordering for free

The message key is set to the pattern. With the default `kafka.Hash` balancer that maps
each subject to a stable partition, and a partition is ordered, so **every message for
one subject arrives in the order it was produced**. It costs nothing and it is the
guarantee people assume they already have.

Replacing `Balancer` with `RoundRobin` or `LeastBytes` gives that up.

## `GroupID` is required to listen

`Listen` returns an error when `GroupID` is empty, rather than starting. Without a
consumer group every replica of a service reads every partition, so every replica
handles every message: the service is not load-balanced, it is fanned out, and the
duplicate side effects usually go unnoticed until production.

A publish-only client needs no group, which is why the field is not required by `New`.

## `Concurrency` defaults to 1

Concurrency and per-partition ordering are mutually exclusive, and ordering is the
reason to be on Kafka, so the default is 1.

Raising it costs two things at once. Two messages with the same key are then handled
simultaneously, so per-subject ordering is gone. And the delivery guarantee weakens:
offsets are committed as each handler finishes, and Kafka tracks a **single watermark
per partition**, so committing message 7 while 5 and 6 are still running marks them
delivered. A warning is logged when `Concurrency` is above 1. Keep it at 1 whenever
ordering or strict at-least-once matters.

## A failed handler commits anyway

When a handler fails, the offset is committed and the stream moves on. That is a
decision, not an oversight:

- Kafka tracks one offset watermark per partition, so "do not commit this one" is not
  expressible. Skipping the commit redelivers the entire uncommitted range after it on
  the next start, re-running every message that already succeeded.
- A message that fails deterministically would then block its partition forever, and
  the partition is the unit of throughput: one bad message stalls every subject that
  hashes to it.

So the failure is logged, the caller gets an error reply when it asked for one, and the
stream continues. **A dead-letter topic is the right escalation** when losing the
message is not acceptable: republish the raw message to `Topic + ".dlq"` and commit.
That belongs in your handler or in a `Config.Middleware` entry, where the payload's
semantics are known — the transport cannot decide for you which failures are worth
keeping.

A message whose bytes do not decode is committed too, because bytes that do not parse
now will not parse on redelivery.

## Request/reply works, and is a poor fit

`Request` needs `Options.ReplyTopic`; without it it returns
`microservice.ErrNotSupported`, because a Kafka topic has no built-in reply address so
replies must be produced to a topic the client consumes.

It works. It also costs a produce round trip, a fetch round trip, and — on the first
call of a process — a consumer-group join, which is typically hundreds of milliseconds.
Use [NATS](nats.md), [RabbitMQ](rabbitmq.md) or [gRPC](grpc.md) for RPC and keep Kafka
for the event log.

Two things to know if you use it anyway. The reply consumer joins a group unique to
this process, because a shared group would load-balance replies across client
instances — a reply would routinely be delivered to a process that never made the
request, and the real requester would time out. The cost is that every client reads
every reply on the topic and discards the ones it is not waiting for. And that means
every client consuming `ReplyTopic` sees every reply on it: correlation is by envelope
id, not by broker-side addressing. Envelope ids are cryptographically random so they
cannot be guessed, but the payloads are visible, so **a reply topic must not be shared
across a trust boundary**.

## `CreateTopics` is off by default

Auto-creating a topic in production gives it whatever partition count and replication
factor the client asked for, and **neither can be reduced afterwards**. A topic created
with one partition caps that stream's throughput permanently; one created with
replication factor 1 loses data when a broker dies. Topics are infrastructure.

Turn it on for local development, where a bare broker and a developer who wants to run
the service is the whole story:

```go
kafkamq.MustNew(kafkamq.Options{
    Brokers:      []string{"localhost:9092"},
    GroupID:      "users",
    CreateTopics: true,
    Partitions:   3,
})
```

An already-existing topic is not an error.

## Testing

Handlers need no cluster. `nikatest.NewMicroservice(t)` runs the whole message stack
over the in-memory transport:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")
```

See [Testing](../fundamentals/testing.md).

The reader and writer configurations are asserted in unit tests without a broker,
because the options that matter most here — `StartOffset`, `CommitInterval`,
`RequiredAcks` — are precisely the ones whose effect is invisible until something is
lost. The rest of the suite needs a live cluster and is behind a build tag:

```bash
KAFKA_BROKERS=localhost:9092 \
  go test -race -tags kafka_integration ./common/microservice/transport/kafkamq/
```

Each run uses a unique topic and group suffix, so offsets from a previous run cannot
leak in.

## When to use it, and when not to

Use Kafka when the stream itself is the product: event sourcing, audit logs, analytics
pipelines, anything where a consumer added next month should be able to read what
happened last month. Retention plus consumer groups means many independent consumers
can read the same stream at their own pace, and replay is a matter of resetting an
offset. Per-subject ordering comes free from the pattern-as-key design.

Do not use it for RPC — the round trips and the group join make it the slowest
request/reply here. Do not use it when you need per-message acknowledgement or a
dead-letter path the broker manages for you: Kafka's single watermark per partition
means a failed message is committed and moved past, where [RabbitMQ](rabbitmq.md) can
nack one delivery and dead-letter it. And Kafka is real operational weight — if you
want events without a cluster to run, [NATS](nats.md) or [Redis](redis.md) is a much
smaller commitment.
