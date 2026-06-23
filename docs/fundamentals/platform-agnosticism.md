# Platform Agnosticism

> **Coming Soon** — This feature is not yet implemented.

Nika is currently tightly coupled to **Gin** as its HTTP engine. The goal of platform agnosticism is to allow Nika to run on different transport layers (HTTP frameworks, gRPC, WebSockets, etc.) without changing the application code.

## Current State

Nika currently uses Gin for all HTTP operations:

```go
// The App struct is directly tied to Gin
type App struct {
    engine    *gin.Engine
    container map[reflect.Type]interface{}
}
```

## Planned Design

```go
// Planned: Platform-agnostic interfaces (subject to change)
type HTTPAdapter interface {
    Listen(addr string) error
    Handle(method, path string, handler interface{})
    Use(middleware ...interface{})
}

// Gin adapter
type GinAdapter struct {
    engine *gin.Engine
}

func (a *GinAdapter) Listen(addr string) error {
    return a.engine.Run(addr)
}

// Fiber adapter (future)
type FiberAdapter struct {
    app *fiber.App
}

func (a *FiberAdapter) Listen(addr string) error {
    return a.app.Listen(addr)
}
```

## Current Limitations

| Platform | Status |
|----------|--------|
| Gin (HTTP) | ✅ Implemented |
| Fiber (HTTP) | ⏳ Planned |
| Chi (HTTP) | ⏳ Planned |
| gRPC | ⏳ Planned |
| WebSockets | ⏳ Planned |

!!! info "Coming Soon"
    Platform-agnostic HTTP adapters are planned for future releases.
