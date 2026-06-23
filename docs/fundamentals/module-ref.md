# Module Reference

> **Coming Soon** — This feature is not yet implemented.

The Module Reference provides a way to access providers from other modules, retrieve the current module instance, or interact with the internal module system.

## Planned Design

```go
// Planned API (subject to change)
type ModuleRef struct {
    container map[reflect.Type]interface{}
}

func (r *ModuleRef) Get(instance interface{}) error {
    // Resolve a provider from the container
    return nil
}

func (r *ModuleRef) Resolve(key string) interface{} {
    // Resolve a named provider
    return nil
}
```

## Current Alternative

Since the DI container is global, you can access any provider that has been registered:

```go
// If you need to access a provider from another module,
// simply depend on it in your constructor:
func NewUserService(
    repo *UserRepository,
    cache *cache.Cache,        // From cache module
    config *config.Config,     // From config module
) *UserService {
    return &UserService{
        repo:   repo,
        cache:  cache,
        config: config,
    }
}
```

## Use Cases

| Use Case | Status |
|----------|--------|
| Access providers from other modules | ✅ Available via DI container |
| Retrieve module instance | ⏳ Planned |
| Dynamic provider resolution | ⏳ Planned |
| Named/qualified bindings | ⏳ Planned |

!!! info "Coming Soon"
    A formal `ModuleRef` API with named bindings and dynamic resolution is planned.
