# Injection Scopes

In Nika, the DI container currently supports **singleton scope** only. This means that once a provider is registered, the same instance is shared across all requests and controllers.

## Singleton Scope

All providers in Nika are singletons by default:

```go
// This provider is created once and shared everywhere
func NewUserService(repo *UserRepository) *UserService {
    return &UserService{repo: repo}
}
```

```
┌─────────────────────────────┐
│           Request 1          │
│   UserController ──────────►│──┐
└─────────────────────────────┘  │
                                 │  Shared *UserService
┌─────────────────────────────┐  │
│           Request 2          │  │
│   UserController ──────────►│──┘
└─────────────────────────────┘
```

## Current Limitations

| Scope | Status |
|-------|--------|
| Singleton (default) | ✅ Implemented |
| Request scope | ⏳ Planned |
| Transient scope | ⏳ Planned |

### Request Scope (Planned)

In a request scope, a new instance of the provider would be created for each incoming HTTP request. This is useful for request-scoped data like the current user context, request logger, etc.

### Transient Scope (Planned)

In a transient scope, a new instance would be created every time it is injected.

## Best Practices

Until request/transient scopes are implemented:

- Keep providers **stateless** when possible
- Use function parameters to pass request-specific data
- Store request-scoped data in `gin.Context` instead of provider fields

```go
// Good — stateless service
func (s *UserService) FindByID(ctx context.Context, id string) (*User, error) {
    // No state stored in the service
    return s.repo.FindOneByID(ctx, id)
}

// Avoid — storing request-specific state in a singleton
func (s *UserService) ProcessRequest(c *gin.Context) {
    s.currentRequest = c // ⚠️ NOT thread-safe in a singleton!
}
```

!!! info "Coming Soon"
    Request and transient injection scopes are planned for future releases.
