# Events

> **Coming Soon** — An event system is not yet implemented.

An event system allows you to publish and subscribe to events within your application, enabling loose coupling between components.

## Planned Design

```go
// Planned API (subject to change)
type EventEmitter interface {
    On(event string, handler func(payload interface{}))
    Emit(event string, payload interface{})
    Off(event string)
}

type EventModule struct {
    emitter EventEmitter
}
```

## Planned Example

```go
func (s *UserService) Create(user *User) *User {
    // Create user
    result := s.repo.Create(user)

    // Emit event
    s.events.Emit("user.created", result)

    return result
}

// Subscribe to events
func (m *NotificationModule) OnModuleInit() {
    m.events.On("user.created", func(payload interface{}) {
        user := payload.(*User)
        m.sendWelcomeEmail(user.Email)
    })
}
```

## Current Alternative

Use a standalone event library or implement a simple event bus:

```go
type EventBus struct {
    handlers map[string][]func(interface{})
    mu       sync.RWMutex
}

func NewEventBus() *EventBus {
    return &EventBus{handlers: make(map[string][]func(interface{}))}
}

func (b *EventBus) On(event string, handler func(interface{})) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.handlers[event] = append(b.handlers[event], handler)
}

func (b *EventBus) Emit(event string, payload interface{}) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, handler := range b.handlers[event] {
        go handler(payload) // Async
    }
}
```

## Status

| Feature | Status |
|---------|--------|
| Event emitter | ⏳ Planned |
| Event listeners | ⏳ Planned |
| Async events | ⏳ Planned |
| Event middleware | ⏳ Planned |
