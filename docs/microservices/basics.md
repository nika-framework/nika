# Microservices Overview

> **Coming Soon** — Microservices support is not yet implemented.

Nika plans to support microservices architecture with various transport layers (Redis, NATS, RabbitMQ, Kafka, gRPC).

## Planned Design

```go
// Planned API (subject to change)
type MicroserviceApp struct {
    transport Transporter
    container map[reflect.Type]interface{}
}

type Transporter interface {
    Listen(pattern string, handler interface{}) error
    Send(pattern string, data interface{}) error
    Publish(event string, data interface{}) error
}
```

## Status

| Transport | Status |
|-----------|--------|
| Redis | ⏳ Planned |
| NATS | ⏳ Planned |
| RabbitMQ | ⏳ Planned |
| Kafka | ⏳ Planned |
| gRPC | ⏳ Planned |
| Custom transporters | ⏳ Planned |

!!! info "Want to contribute?"
    Microservices support is open for contribution.
