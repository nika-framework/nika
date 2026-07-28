# gRPC

Nika ships **two gRPC packages**, and they make opposite trades. Both are
protoc-free, but only one of them is a protobuf contract:

| Package | What it is | Use it for |
|---|---|---|
| [`common/grpcapi`](#grpc-api) | A **real protobuf API**, with descriptors derived from your structs at startup and served over reflection | a public or polyglot contract that generated clients call |
| [`transport/grpcmq`](#grpc-transport) | A **JSON-envelope transport** that reuses the microservice handler stack over gRPC | Go-to-Go service-to-service calls inside the framework |

Many services run both, on the same port, for two different callers. Jump to the
half you need:

- **[gRPC API](#grpc-api)** — `common/grpcapi`, schema and reflection, no `.proto`.
- **[gRPC transport](#grpc-transport)** — `transport/grpcmq`, envelope over gRPC.

See [Microservices Overview](basics.md) for the shared envelope, patterns and
handler model that the transport half builds on.

---

## gRPC API

`common/grpcapi` serves a real gRPC API defined in Go — **no `.proto` file and no
code-generation step**.

The protobuf descriptors are derived from your request and response structs when
the app starts, registered in a descriptor registry, and served over gRPC server
reflection. A Python, NestJS, Java or Bruno client discovers the schema exactly as
it would for a protoc-generated service, and speaks ordinary protobuf on the wire.
There is no custom codec and no envelope.

!!! note "This is not the gRPC *transport*"
    [`transport/grpcmq`](#grpc-transport) carries the framework's JSON envelope and
    routes on a `pattern`, which is what lets one handler also run over Redis and
    TCP — and is exactly why a generated gRPC client cannot call it. This package
    makes the opposite trade: a genuine protobuf contract for one protocol.

### Define a service

```go
package rpc

import (
    "context"
    "time"

    "github.com/nika-framework/nika/common/grpcapi"
)

type GetUserRequest struct {
    ID int64 `json:"id" protobuf:"1"`
}

type User struct {
    ID        int64     `json:"id" protobuf:"1"`
    Username  string    `json:"username" protobuf:"2"`
    CreatedAt time.Time `json:"created_at" protobuf:"3"`

    // Excluded from the contract entirely.
    PasswordHash string `json:"-"`
}

type UserController struct {
    service *services.UserService

    GetUser grpcapi.Unary[GetUserRequest, User] `grpc:"GetUser"`
}

func (c *UserController) GRPCService() string { return "UserService" }

func NewUserController(service *services.UserService) *UserController {
    c := &UserController{service: service}
    c.GetUser = func(ctx context.Context, in *GetUserRequest) (*User, error) {
        user, err := c.service.FindOne(ctx, in.ID)
        if err != nil {
            return nil, grpcapi.Internal("could not read the user")
        }
        if user == nil {
            return nil, grpcapi.NotFound("no user with id %d", in.ID)
        }
        return toWire(user), nil
    }
    return c
}
```

Register it like any other controller — the module system already carries it:

```go
func (m *UserModule) Controllers() []interface{} {
    return []interface{}{rpc.NewUserController}
}
```

### Wire it up

```go
grpcapi.Setup(app, grpcapi.Config{
    Addr:     ":50052",
    Package:  "user.v1",
    Insecure: true,   // must be explicit; there is no implicit plaintext
})
```

`Package` is the protobuf namespace clients see, so it is a published name —
include a version, and treat changing it as breaking every generated client.

Handlers take and return typed values rather than a `*gin.Context`, because a gRPC
call has no URL, no query string and no HTTP status; mapping it onto an HTTP
context would misrepresent all three.

### Call it from anywhere

```bash
grpcurl -plaintext 127.0.0.1:50052 list
# grpc.reflection.v1.ServerReflection
# user.v1.UserService

grpcurl -plaintext 127.0.0.1:50052 describe user.v1.User
# message User {
#   int64 id = 1;
#   string username = 2;
#   .google.protobuf.Timestamp created_at = 3;
# }

grpcurl -plaintext -d '{"id":1}' 127.0.0.1:50052 user.v1.UserService/GetUser
```

That schema was reconstructed by the client from reflection. No `.proto` file
exists anywhere in the project.

**Bruno**: set the URL to `127.0.0.1:50052`, TLS off, and click load methods.

**Python**, with no generated stubs:

```python
import grpc
from grpc_reflection.v1alpha.proto_reflection_descriptor_database import (
    ProtoReflectionDescriptorDatabase)
from google.protobuf.descriptor_pool import DescriptorPool
from google.protobuf.message_factory import GetMessageClass

channel = grpc.insecure_channel("127.0.0.1:50052")
pool = DescriptorPool(ProtoReflectionDescriptorDatabase(channel))

request_type = GetMessageClass(pool.FindMessageTypeByName("user.v1.GetUserRequest"))
reply_type = GetMessageClass(pool.FindMessageTypeByName("user.v1.User"))

call = channel.unary_unary(
    "/user.v1.UserService/GetUser",
    request_serializer=request_type.SerializeToString,
    response_deserializer=reply_type.FromString,
)
print(call(request_type(id=1)).username)
```

**NestJS**, which wants a file, can be given one — dump the derived descriptors:

```go
files := server.Files()   // *protoregistry.Files
```

### Type mapping

| Go | protobuf |
|---|---|
| `string` | `string` |
| `bool` | `bool` |
| `int`, `int64` | `int64` |
| `int8/16/32` | `int32` |
| `uint`, `uint64` | `uint64` |
| `uint8/16/32` | `uint32` |
| `float32` / `float64` | `float` / `double` |
| `[]byte` | `bytes` |
| `time.Time` | `google.protobuf.Timestamp` |
| nested `struct` | nested message |
| `[]T` | `repeated T` |
| `*T` | `optional T` (presence tracked) |
| `map[...]` | **unsupported** — fails at startup |
| `interface{}` | **unsupported** — fails at startup |

A field name comes from its `json` tag, falling back to snake_case of the Go name.
`json:"-"` or `grpc:"-"` leaves the field out of the contract entirely — which is
how a password hash stays off the wire structurally, rather than by remembering to
strip it.

!!! warning "Pin field numbers on anything published"
    Numbers come from declaration order unless a `protobuf:"N"` tag sets them.
    Reordering two struct fields then silently changes the wire format for every
    client already generated against it — old clients read the new bytes into the
    wrong fields, with no error anywhere. Add `protobuf:"N"` before anyone else
    depends on the schema.

### Presence

A Go pointer becomes proto3 `optional`, so an absent field and a field set to the
zero value stay distinguishable:

```go
type UpdateUserRequest struct {
    ID       int64   `json:"id" protobuf:"1"`
    Username *string `json:"username" protobuf:"2"`  // nil means "leave alone"
}
```

Without it, "clear the username" and "do not touch the username" are the same
request — which is why a PATCH-shaped API needs pointers.

### Errors

A gRPC client branches on the status code, so returning the right one is not
cosmetic:

```go
return nil, grpcapi.NotFound("no user with id %d", id)
return nil, grpcapi.InvalidArgument("email must be a valid address")
return nil, grpcapi.AlreadyExists("that email is taken")
return nil, grpcapi.PermissionDenied("not your account")
return nil, grpcapi.Unavailable("the upstream is down")
return nil, grpcapi.Internal("could not read the user")
```

Any `status.Error` is passed through untouched. **Any other error becomes
`Internal` with a generic message** — a raw database error can carry a DSN, a
table layout or an internal hostname, so it never reaches the caller. Log the
cause yourself, or add a logging interceptor.

### Validation

Wire the framework validator in and the same `validate:"..."` tags that produce a
422 over REST produce `InvalidArgument` here:

```go
grpcapi.Setup(app, grpcapi.Config{
    ...
    Validate: func(request any) error {
        if errs := validator.ValidateStruct(request); len(errs) > 0 {
            return grpcapi.InvalidArgument("%s: %s", errs[0].Field, errs[0].Message)
        }
        return nil
    },
})
```

It runs on the decoded request before the handler, so a rejected call never
reaches your code.

### Configuration

| Field | Default | Notes |
|---|---|---|
| `Addr` | `:50051` | listen address; `:0` picks a free port, read it from `Addr()` |
| `Package` | — | **required**; the protobuf namespace |
| `Insecure` | `false` | must be explicit to serve plaintext |
| `Creds` | — | transport credentials for TLS |
| `DisableReflection` | `false` | reflection is on by default; turn it off on a public port |
| `MaxRecvMsgSize` | 8 MiB | grpc-go's own 4 MiB default silently rejects larger payloads |
| `KeepaliveMinTime` | 30s | without an enforcement policy a client can ping in a tight loop |
| `GracefulStopTimeout` | 15s | `GracefulStop` alone can hang on a stuck stream |
| `UnaryInterceptors` | — | ordinary gRPC interceptors |
| `RegisterServices` | — | register a hand-written or generated service on the same port |
| `Validate` | — | runs before every handler |

### What this does not do yet

- **Streaming.** Unary only. Server, client and bidirectional streams are not
  derived.
- **Maps and `interface{}`.** Both fail at startup with a named field rather than
  producing a lossy schema.
- **Enums.** A Go constant type maps to its underlying integer, not a protobuf
  enum.
- **Breaking-change detection.** There is no `.proto` in version control to diff,
  which is the real cost of this approach: nothing stops you renaming a field.
  Pin field numbers, and if the contract is consumed outside your team, consider
  dumping the descriptors into CI and diffing them.

When you need any of those, write a `.proto`, generate it, and register it through
`Config.RegisterServices` — the two coexist on one port.

---

## gRPC transport

`transport/grpcmq` is the framework's [microservice](basics.md) transport over
gRPC. It gives you the lowest-latency request/reply of any transport here — no
correlation map, no broker hop, HTTP/2 multiplexing, deadlines propagated by the
protocol itself, and mTLS — with **no `.proto` file and no code generation step**.
The service is a hand-written `grpc.ServiceDesc` and the JSON envelope travels
through a registered pass-through codec. The thing to internalise first: gRPC is
not a broker, so there is nothing to hold a message while the far side is down.

### Delivery guarantees

There are none, and the difference from every other transport in this section is
not a detail:

- **There is no store and forward.** If the server is down when you `Publish`, the
  message is lost. There is no queue to hold it and nothing to retry into.
- **`Publish` still costs a full round trip.** It is fire-and-forget only in the sense
  that the reply is discarded; the TCP round trip and the server's handler both happen
  before `Publish` returns, and a server that refuses the message returns an error.
- **There is no fan-out.** One call reaches one server.

Use gRPC for synchronous service-to-service calls, where a caller is waiting and a
failure should be visible immediately. Use [Kafka](kafka.md), [NATS](nats.md) or
[RabbitMQ](rabbitmq.md) for events, where the publisher must not care whether a
consumer exists yet.

`Request` is a plain unary call: the RPC itself is the correlation, which is why this is
the cheapest request/reply of the set.

### Minimal working setup

A server:

```go
package main

import (
    "github.com/gin-gonic/gin"
    "google.golang.org/grpc/credentials"

    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/microservice"
    "github.com/nika-framework/nika/common/microservice/transport/grpcmq"

    "myapp/src"
)

func main() {
    app := nika.NewApp()

    creds, err := credentials.NewServerTLSFromFile("server.crt", "server.key")
    if err != nil {
        panic(err)
    }

    microservice.Setup(app, microservice.Config{
        Transport: grpcmq.MustNew(grpcmq.Options{
            Addr:  ":9000",
            Creds: creds,
        }),
        Middleware: []gin.HandlerFunc{nika.RecoveryMiddleware()},
    })

    app.LoadModule(src.NewAppModule())
    app.Listen(":3001")
}
```

Handlers declare `transport:"grpc"`:

```go
type UserController struct {
    Create  func(*gin.Context) `transport:"grpc" pattern:"user_created"`
    FindOne func(*gin.Context) `transport:"grpc" pattern:"user_*"`
}
```

A client, handling the error:

```go
transport, err := grpcmq.New(grpcmq.Options{
    Target:    "dns:///users.internal:9000",
    TLSConfig: tlsConfig,
})
if err != nil {
    return fmt.Errorf("grpc transport: %w", err)
}

client, err := microservice.SetupClient(app, transport)
if err != nil {
    return err
}
```

One transport can do both halves — set `Addr` and `Target` together, and the same value
serves handlers and makes calls. `New` neither listens nor dials; both happen on first
use, so a peer that is briefly down does not stop this process from starting.
Configuration errors are reported by `New`, because a wiring mistake should stop the
process at startup rather than surface as a failed call under load.

### Options

#### Server

| Field | Default | Purpose |
|---|---|---|
| `Addr` | — | Listen address, e.g. `":9000"` or `"127.0.0.1:0"`. **Required by `Listen`.** Port 0 asks the OS for a free port; read it back with `Addr()`. |
| `MaxRecvMsgSize` | `8 MiB` (`DefaultMaxRecvMsgSize`) | Bounds an inbound message, and the send side symmetrically. |
| `MaxConcurrentStreams` | `0` (grpc-go's default) | Caps concurrent streams **per HTTP/2 connection**, so it is not a process-wide limit. |
| `KeepaliveMinTime` | `30s` (`DefaultKeepaliveMinTime`) | Minimum interval between client keepalive pings the server accepts. A client that pings faster is disconnected with `ENHANCE_YOUR_CALM`. |
| `KeepalivePermitWithoutStream` | `false` | Allow pings on an idle connection. Permitting them lets an idle client keep a connection and its resources alive indefinitely for free. |
| `ConnectionTimeout` | `20s` (`DefaultConnectionTimeout`) | Bounds connection setup, including the TLS handshake, so a half-open connection cannot occupy a slot indefinitely. |
| `Concurrency` | `256` (`DefaultConcurrency`) | Handlers running at once across every connection. Negative means no limit. |
| `UnaryInterceptors`, `StreamInterceptors` | `nil` | Applied in order. These are ordinary gRPC interceptors and work with anything from the ecosystem. |
| `ServerOptions` | `nil` | Appended last, so they can override anything above. |
| `GracefulStopTimeout` | `15s` (`DefaultGracefulStopTimeout`) | Bounds a graceful shutdown before it is forced. |

#### Client

| Field | Default | Purpose |
|---|---|---|
| `Target` | — | Address to call, in gRPC target syntax (`host:port`, `dns:///svc:9000`, `unix:///tmp/s.sock`). **Required by `Publish` and `Request`.** |
| `DialTimeout` | `10s` (`DefaultDialTimeout`) | Bounds the wait for a usable connection on the first call. |
| `ClientKeepalive` | zero `keepalive.ClientParameters` | Client-side pings. Keep `Time` at or above the server's `KeepaliveMinTime` or the server drops the connection. Only applied when `Time` is above zero. |
| `WaitForReady` | `false` | Wait for a healthy connection instead of failing fast with `Unavailable`. Right for a transient rollout, wrong when a fast failure is the useful signal. |
| `ReplyTimeout` | `microservice.DefaultRequestTimeout` (10s) | Request deadline when the caller passes none. |
| `DialOptions` | `nil` | Appended last. Use them for retry policies via `grpc.WithDefaultServiceConfig`. |

#### Both halves

| Field | Default | Purpose |
|---|---|---|
| `Creds` | `nil` | `credentials.TransportCredentials` for both halves. Wins over `TLSConfig`. |
| `TLSConfig` | `nil` | Builds credentials when `Creds` is nil. |
| `Insecure` | `false` | **Must be explicitly true** to run without transport security. |
| `Logger` | `slog.Default()` | Receives malformed-message and lifecycle events. |

`New` requires at least one of `Addr` and `Target`.

### Plaintext cannot ship by accident

!!! warning "`New` fails unless transport security is configured or `Insecure` is explicitly true"
    There is no implicit plaintext default. Set `Creds`, or `TLSConfig`, or set
    `Insecure: true` to accept plaintext gRPC — and `New` returns an error naming all
    three if you set none of them.

gRPC's own history of accidentally-plaintext production deployments is why grpc-go
itself now requires an explicit insecure credential, and repeating that requirement here
means nobody ships unencrypted traffic by forgetting a field. A message transport
carries auth headers and tenant ids, so this matters more here than for a typical API.

For a local test or a loopback sidecar, say so out loud:

```go
grpcmq.MustNew(grpcmq.Options{
    Addr:     "127.0.0.1:0",
    Insecure: true, // deliberate: loopback only
})
```

### The protoc-free codec, and what it costs

gRPC does not care what the message bytes mean. It needs a codec that can turn a Go
value into bytes and back, and it selects one **by content-subtype**. This transport
registers a codec named `nika-raw` that passes bytes through untouched, and every call
sets `grpc.CallContentSubtype("nika-raw")`. On the wire that appears as
`application/grpc+nika-raw`, so a proxy or a packet capture can tell these RPCs apart
from protobuf ones.

The codec is selected **per call**, not forced on the server. A protobuf service
registered on the same `grpc.Server` by other code keeps working — forcing the codec
would break every one of them.

The service is described by hand rather than generated. A `grpc.ServiceDesc` is just
data: a fully-qualified service name, the method names, and a function per method that
decodes a request, calls the implementation and hands back a response. protoc produces
exactly that struct and nothing magic. The methods are ordinary gRPC paths, so grpcurl,
a service mesh, an interceptor or an access log sees an ordinary service:

| Constant | Value |
|---|---|
| `grpcmq.ServiceName` | `nika.microservice.v1.Messenger` |
| `grpcmq.MethodDispatch` | `/nika.microservice.v1.Messenger/Dispatch` — unary, one envelope in, one out |
| `grpcmq.MethodStream` | `/nika.microservice.v1.Messenger/Stream` — bidirectional |
| `grpcmq.CodecName` | `nika-raw` |

The trade-off against real protobuf is deliberate:

- **Lost:** a schema, generated clients in other languages, field-level compatibility
  checking, and protobuf's compactness.
- **Gained:** no codegen step, no `.pb.go` files to keep in sync with the envelope
  definition, one wire format across every transport in this framework — the same bytes
  travel over Redis, NATS, Kafka and AMQP — and a payload you can read in a packet
  capture.

This transport carries an internal, already-JSON envelope between Go services
that share the `microservice` package, so the schema and codegen benefits have
nobody to serve. **A public, polyglot API is the case where protobuf wins and
this approach should not be used** — use the [gRPC API](#grpc-api) half above,
or write a real `.proto` service alongside it.

### `MaxRecvMsgSize` defaults to 8 MiB, not gRPC's 4

gRPC's own default is 4 MiB, which rejects a larger envelope with `ResourceExhausted` —
a failure that shows up the first time a real payload is big and looks like a bug in the
handler. The default here is 8 MiB, matching the framework's envelope cap, and it is
applied symmetrically on the send side too: a server that accepts an 8 MiB question but
cannot send an 8 MiB answer fails in a way that looks like the handler's fault.

### Keepalive is enforced

Without an enforcement policy a client may ping in a tight loop, which is a cheap way to
burn a server's CPU from outside. The server accepts pings no more often than
`KeepaliveMinTime` (30s) and, by default, refuses them entirely on a connection with no
active stream. A client whose `ClientKeepalive.Time` is below the server's minimum gets
its connection dropped with `ENHANCE_YOUR_CALM`, which is why no client keepalive
interval is set unless you ask for one — a default there would be a trap.

### Shutdown is bounded

Cancelling the context passed to `Listen` drains gracefully; `Close` is the abrupt path.

`GracefulStop` stops accepting connections and waits for every active RPC and stream to
finish — **with no timeout of its own**. One client holding a bidirectional stream open,
or one handler that never returns, blocks it forever, which turns a rolling deploy into
a stuck pod. So the graceful stop is given `GracefulStopTimeout` (15s), and then the
server is forced down with a logged warning. A shutdown that always terminates is worth
more than one that is always graceful.

### Concurrency

`MaxConcurrentStreams` is per connection, so N clients can still produce
N × `MaxConcurrentStreams` handlers at once. `Concurrency` (256) is the process-wide
backstop. When the server is saturated and the caller's deadline passes, the call is
refused with `ResourceExhausted` rather than queueing forever.

A malformed envelope on the unary method fails that one call with `InvalidArgument`,
which correctly tells the caller not to retry — the same bytes will fail again. On a
stream it is answered with a `400` / `MALFORMED_ENVELOPE` error envelope instead of
failing the stream, because a stream carries many messages and killing it over one bad
frame would take down every other in-flight message on it.

### Serving a real protobuf contract alongside

The `nika-raw` codec is what removes protoc from the build, and the price is
that the Messenger service is not a protobuf contract. A Node, Java or Python
client generated from a `.proto` file cannot call it: it would address
`/UserService/GetUserProfile`, while this server exposes
`/nika.microservice.v1.Messenger/Dispatch` and speaks `application/grpc+nika-raw`.
Routing here lives in the envelope's `pattern` field, not in the method name —
which is exactly what lets one handler also serve Redis and NATS.

When you need a genuine contract for other languages, you have two choices:

1. Use the [gRPC API](#grpc-api) half of this page, which derives a real
   protobuf contract from your structs.
2. Generate the service as usual from a `.proto` file and register it on the
   same server:

```go
transport, err := grpcmq.New(grpcmq.Options{
    Addr:     ":50051",
    Insecure: true,
    RegisterServices: func(srv *grpc.Server) {
        userpb.RegisterUserServiceServer(srv, &userServer{})
    },
})
```

Both services then share one port, one set of credentials and one interceptor
chain. They coexist because the codec is resolved per call from the
content-subtype rather than forced server-wide: a protobuf client is served by the
protobuf codec, a Nika client by `nika-raw`, over the same connection.

`RegisterServices` runs after the Messenger is registered, and grpc-go panics on a
duplicate service name — so a collision fails at startup instead of silently
shadowing something.

Use it when you have a polyglot or public API. For Go-to-Go calls between services
that already share the `microservice` package, the envelope needs no schema and
this adds only codegen.

### Testing

Handlers need no server. `nikatest.NewMicroservice(t)` runs the whole message stack over
the in-memory transport:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", CreateUserDto{Name: "Ada"}).
    ExpectStatus(201).
    ExpectJSONPath("data.name", "Ada")
```

See [Testing](../fundamentals/testing.md).

The transport's own tests are **fully self-contained** — there is no broker, so they
bind `127.0.0.1:0` and need no build tag and no environment variable:

```bash
go test -race ./common/microservice/transport/grpcmq/
```

`Options.Addr: "127.0.0.1:0"` plus `Ready()` and `Addr()` is the pattern for your own
end-to-end tests: `Ready()` closes once the listener is bound, so you can learn the
OS-assigned port without polling. Note that with port 0 the OS assigns a fresh port on
every `Listen`, so the value changes across a supervisor restart — use a fixed port in
production.

### When to use it, and when not to

Use gRPC for synchronous service-to-service calls where a caller is blocked on the
answer: the lowest latency of the set, mTLS, deadlines carried by the protocol,
HTTP/2 multiplexing over one connection, and interceptors that work with the existing
ecosystem. If you already run a service mesh, this is the transport it understands.

Do not use it for events. There is no queue, no fan-out and no retry, so a
publish against a server that is redeploying is simply lost — use
[Kafka](kafka.md), [NATS](nats.md) or [RabbitMQ](rabbitmq.md), where the broker
holds the message. Do not use it for a public or polyglot API either: use the
[gRPC API](#grpc-api) half above, or write a real protobuf service instead, since
the pass-through codec deliberately has no schema. And if you want a synchronous
transport with no broker *and* no gRPC dependency — a sidecar, an embedded pair,
a test — [TCP](tcp.md) is the smaller answer.
