# TCP Transporter

The TCP transport has no broker in the middle: the server binds a port and clients dial
it directly, exchanging length-prefixed JSON envelopes over a long-lived connection. It
is the smallest possible thing that satisfies the transport contract, which makes it the
right choice in exactly two situations — a sidecar or tightly coupled pair of services
where a broker would be the only piece of infrastructure in the deployment, and tests,
because a TCP transport needs nothing running.

See [Microservices Overview](basics.md) for patterns, handlers and the envelope.

## Delivery guarantees

There are none, for the same reason as [gRPC](grpc.md): this is synchronous, with no
broker and no buffering.

- **No persistence.** If the server is down when you publish, the message is lost.
  Nothing holds it and nothing retries it.
- **No fan-out.** One connection reaches one server.
- **No queue to absorb a consumer restart.** The client redials, but a message written
  into a dying connection is gone.
- **The client must know the server's address.** There is no discovery.

If a message must survive the consumer being down, use a broker-backed transport —
[RabbitMQ](rabbitmq.md) or [Kafka](kafka.md).

Request/reply works and is cheap: the reply travels back on the connection that carried
the request, correlated by `Envelope.ID`. Because there is no broker to filter at, a
pattern that matches nothing still gets a `404` / `PATTERN_NOT_FOUND` reply rather than
being dropped silently — the sender is on the other end of this very connection and can
be told.

## Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/tcpmq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    microservice.Setup(app, microservice.Config{
        Transport: tcpmq.MustNew(tcpmq.Options{
            Addr:      ":4000",
            TLSConfig: tlsConfig, // anything crossing a network boundary should set this
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"tcp"`:

```go
type UserController struct {
    Create  func(*gin.Context) `transport:"tcp" pattern:"user_created"`
    FindOne func(*gin.Context) `transport:"tcp" pattern:"user_*"`
}
```

A client, handling the error:

```go
transport, err := tcpmq.New(tcpmq.Options{
    DialAddr:        "users.internal:4000",
    ClientTLSConfig: clientTLS,
})
if err != nil {
    return fmt.Errorf("tcp transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

One transport does both halves. A process that only serves needs `Addr`; a process that
only publishes needs `Addr` or `DialAddr` pointing at the server; a process doing both
needs nothing extra. `New` neither binds nor dials, because a TCP transport is normally
constructed while wiring the application, before either peer is listening — failing on an
unreachable address there would make startup order significant. Configuration mistakes
are caught by `New`; connectivity problems surface from `Listen`, `Publish` or `Request`,
where the supervisor can retry them.

## Options

| Field | Default | Purpose |
|---|---|---|
| `Addr` | — | Bind address for `Listen` (`":4000"` binds all interfaces, `"127.0.0.1:0"` binds a free loopback port). Also the default dial target. **Required unless `DialAddr` is set.** |
| `DialAddr` | `Addr` | Overrides `Addr` for the client half, for a service that binds `":4000"` but reaches its peer at `"peer.internal:4000"`. |
| `TLSConfig` | `nil` | Wraps the listener with `tls.NewListener`. Without it the transport carries auth headers and tenant ids in clear text. |
| `ClientTLSConfig` | `TLSConfig` | Used when dialing. The default is right for symmetric mTLS and wrong for a server-only certificate — set it explicitly when the two differ. |
| `MaxFrameBytes` | `8 MiB` (`DefaultMaxFrameBytes`) | Bounds one inbound frame. May not *exceed* the default, because a larger frame could never decode into an envelope anyway. |
| `MaxConns` | `1024` | Caps simultaneously served connections. |
| `Concurrency` | `64` | Messages dispatched at once across all connections. |
| `ReadTimeout` | `30s` | Bounds reading a frame body once its header has arrived. The slow-loris guard. |
| `WriteTimeout` | `10s` | Bounds writing one reply frame. |
| `IdleTimeout` | `5m` | How long a connection may sit between frames. Safe to keep short — the client redials transparently. |
| `DialTimeout` | `5s` | Bounds one dial attempt. |
| `MaxDialAttempts` | `3` | Bounds the redial loop for a single `Publish` or `Request`. Also bounded by the caller's context. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request deadline when the caller passes none. |
| `ShutdownTimeout` | `5s` | How long `Close` waits for in-flight handlers. |
| `OnListen` | `nil` | Called with the bound address immediately after the listener is created. The only way to learn the port when binding to `:0`. |
| `Logger` | `slog.Default()` | Receives decode failures and connection errors. |

`New` rejects a negative `MaxFrameBytes`, `MaxConns` or `Concurrency`, a
`MaxFrameBytes` above 8 MiB, and both `Addr` and `DialAddr` being empty.

## The wire format, and the allocation order that matters

A frame is a **4-byte big-endian length prefix followed by exactly that many bytes of
JSON envelope**. A length prefix rather than a delimiter, because the payload is JSON and
JSON can legally contain any byte inside a string once escaped: a delimiter would need
escaping and unescaping on every frame, and getting that wrong is a framing
desynchronisation bug that only appears under unusual payloads. With a length prefix the
reader always knows exactly how many bytes belong to the current message, so a malformed
*payload* never desynchronises the *stream* — it is logged and the next frame is read.

!!! warning "The announced length is validated before anything is allocated"
    The length prefix is attacker controlled. `make([]byte, n)` with an unvalidated `n` is
    a one-packet remote memory-exhaustion attack: four bytes of `0xFF` ask the process for
    4 GiB. The length is therefore checked against `MaxFrameBytes` **before** the body
    buffer exists, and the comparison is done in `uint64` so it is also correct on 32-bit
    platforms, where converting a large `uint32` to `int` would produce a negative number
    and pass a naive `n > max` check.

A frame beyond the limit (`tcpmq.ErrFrameTooLarge`) is fatal for the connection: the body
is deliberately *not* skipped, because the announced length cannot be trusted, so the
stream position is unknowable from that point. A zero-length frame
(`tcpmq.ErrZeroFrame`) cannot be a valid envelope and almost always means a peer speaking
a different protocol.

## `MaxConns` and the per-connection deadlines

An unbounded accept loop is a file-descriptor exhaustion vector: a peer that opens
connections and never speaks costs the process one fd each until `accept()` starts failing
for everyone. `MaxConns` (1024) caps it, and the slot is taken **before** `Accept`, not
after. Accepting first and then closing over the limit still costs an fd per attacker
connection and turns the accept loop into a busy loop; leaving the connection in the
kernel's backlog costs nothing and applies real backpressure at the TCP layer.

Three deadlines defend a connection that has already been accepted:

- **`IdleTimeout` (5m)** is armed while waiting for the next header. It is the only thing
  that reclaims a connection from a peer that completed the handshake — TLS included — and
  then went silent.
- **`ReadTimeout` (30s)** is armed once a header has arrived and the length is known. This
  is the slow-loris guard: a peer that announces a 1 MiB frame and then sends one byte per
  minute would otherwise hold a connection slot forever.
- **`WriteTimeout` (10s)** bounds one reply frame, so a peer that stops reading cannot
  block a handler goroutine indefinitely once the socket buffer fills.

Reaching `Concurrency` applies backpressure by pausing reads, which is the behaviour you
want: TCP's own window then slows the publisher down instead of the process buffering an
unbounded backlog in memory.

## The client multiplexes one connection

A fresh connection per request would be simpler and correct, but it pays a TCP handshake —
and a TLS handshake — for every message, and it burns a local port per in-flight call, so
a burst of requests can exhaust the ephemeral port range and then sit in `TIME_WAIT` for a
minute.

So one connection is shared. Every request writes onto it, a **single background reader**
demultiplexes inbound frames by `Envelope.ID` onto the goroutines waiting for them, and
the connection is redialled on demand when it dies. One reader per connection is what
makes a shared connection safe to read at all: two readers on one socket would each get a
fragment of every frame.

Redialling is deliberate rather than incidental. The peer of a broker-less transport is an
ordinary process that restarts, so without a retry loop every deploy of the server would
surface as a hard error in the client instead of a brief pause. `MaxDialAttempts` (3)
bounds it with exponential backoff, and the caller's context bounds it too.

A lost connection returns `tcpmq.ErrConnLost`, which is deliberately distinct from
`microservice.ErrTimeout`:

```go
err := client.Send(ctx, "user_find", FindDto{ID: 23}, &user)

switch {
case errors.Is(err, tcpmq.ErrConnLost):
    // The connection died. This says nothing about whether the server
    // processed the message — you must decide whether retrying is safe.
case errors.Is(err, microservice.ErrTimeout):
    // Nobody answered in time.
}
```

## Concurrent writes are serialised

Both halves guard frame writes with a per-connection mutex, because a frame is two writes
— the header and the body. Handlers run concurrently and all reply on one socket, and on
the client side concurrent requests share one socket too. An interleaved write from
another goroutine between the header and the body would splice a foreign payload into this
frame's declared length.

That is not a lost message. It is a **permanently desynchronised stream**: the peer reads a
length prefix followed by another message's payload, and every frame after it is corrupt.
Hence the mutex, and hence a failed write retires the connection rather than retrying on
it — a failed write leaves an unknown number of bytes on the wire, so the stream can no
longer be trusted.

## Shutdown keeps the socket writable

When `Listen`'s context is cancelled, a per-connection watchdog unblocks the read loop by
**expiring the read deadline** rather than by closing the socket — a goroutine parked in
`Read` cannot be cancelled any other way. That difference is the whole graceful-shutdown
story: the socket stays writable, so handlers that are already running still deliver their
replies, and only then is the connection closed. Closing it immediately would turn every
in-flight request into a client-side timeout on every deploy.

`ShutdownTimeout` (5s) bounds the wait. A peer that refuses to release its connection is
then force-closed, because `Listen` holding open means the supervisor never rebinds.

## Testing

Handlers need no listener at all. `nikatest.NewMicroservice(t)` runs the whole message
stack over the in-memory transport:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")
```

See [Testing](../fundamentals/testing.md).

The transport's own tests are **fully self-contained** — there is no broker, so they bind
`127.0.0.1:0` and need no build tag and no environment variable:

```bash
go test -race ./common/microservice/transport/tcpmq/
```

That is also the pattern for an end-to-end test of your own stack: bind `127.0.0.1:0`,
learn the port from `OnListen` or `Addr()`, and point a client at it. An end-to-end test of
the whole microservice path then costs a loopback listener instead of a
`docker-compose.yml`.

```go
addrs := make(chan net.Addr, 1)
transport := tcpmq.MustNew(tcpmq.Options{
    Addr:     "127.0.0.1:0",
    OnListen: func(addr net.Addr) { addrs <- addr },
})
```

## When to use it, and when not to

Use TCP for a sidecar or a tightly coupled pair of services where a broker would be the
only piece of infrastructure in the deployment, and for tests that need a real network hop
without a real dependency. It is also the transport to reach for when you want a
synchronous link with no broker *and* no gRPC dependency: one length prefix, one JSON
envelope, no codegen, no HTTP/2.

Do not use it for events, for anything that must survive the consumer being down, or for
fan-out to several consumers — a broker-backed transport does all three and this does none
of them. Prefer [gRPC](grpc.md) when you want the same synchronous shape but with
HTTP/2 multiplexing, protocol-level deadlines, standard interceptors and something a
service mesh understands. And set `TLSConfig` on anything that crosses a network boundary:
the framing is plain JSON, so envelope headers — auth tokens included — are readable to
anyone on the path.
