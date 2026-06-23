# Circular Dependency

> **Coming Soon** — Circular dependency detection is not yet implemented.

Circular dependency occurs when two or more providers depend on each other, directly or indirectly, forming a cycle.

## The Problem

```go
func NewServiceA(serviceB *ServiceB) *ServiceA {
    return &ServiceA{b: serviceB}
}

func NewServiceB(serviceA *ServiceA) *ServiceB {
    return &ServiceB{a: serviceA}
}

// ❌ This creates an infinite loop:
// ServiceA needs ServiceB
// ServiceB needs ServiceA
// ServiceA needs ServiceB
// ...
```

## Current Behavior

Currently, Nika will **panic** when it encounters a circular dependency:

```
❌ DI Error: Cannot resolve '*ServiceA' for constructor
```

## Solutions

### Forward Reference (Planned)

```go
// Planned: Lazy resolution
func NewServiceA(lazy *LazyServiceB) *ServiceA {
    return &ServiceA{b: lazy.Get()}
}
```

### Current Workarounds

**1. Restructure dependencies**

Extract shared logic into a third service:

```go
// Instead of A → B → A
// Create: A → SharedService ← B
func NewSharedService() *SharedService {
    return &SharedService{}
}

func NewServiceA(shared *SharedService) *ServiceA {
    return &ServiceA{shared: shared}
}

func NewServiceB(shared *SharedService) *ServiceB {
    return &ServiceB{shared: shared}
}
```

**2. Use interfaces to break the cycle**

```go
type ServiceAInterface interface {
    DoSomething()
}

type ServiceB struct {
    serviceA ServiceAInterface // Interface, not concrete type
}

func NewServiceB(a ServiceAInterface) *ServiceB {
    return &ServiceB{serviceA: a}
}
```

## Status

| Feature | Status |
|---------|--------|
| Circular dependency detection | ⏳ Planned |
| Lazy loading providers | ⏳ Planned |
| Forward references | ⏳ Planned |
