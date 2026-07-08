# Providers

Providers are the fundamental building blocks of Nika. A **Provider** is essentially a service, repository, factory, helper, or any other object that can be injected into controllers and other providers through the DI container.

## Core Concept

In Nika, almost everything is treated as a Provider — services, repositories, factories, and helpers. Providers are plain Go structs or constructor functions that are registered in the DI container.

## Creating a Provider

### Simple Service

A provider can be as simple as a struct with methods:

```go
package src

type UserService struct{}

func NewUserService() *UserService {
    return &UserService{}
}

func (s *UserService) FindAll() []string {
    return []string{"Alice", "Bob", "Charlie"}
}

func (s *UserService) FindByID(id string) *User {
    // find user logic
    return nil
}
```

### Provider with Dependencies

Providers can depend on other providers. Nika's DI container automatically resolves these dependencies:

```go
package src

import (
    "context"
    "github.com/nika-framework/nika/common/mongodb/repository"
)

type UserRepository struct {
    repo *repository.BaseRepository[User]
}

// Nika automatically resolves *mongo.Collection from the DI container
func NewUserRepository(db *mongo.Database) *UserRepository {
    return &UserRepository{
        repo: repository.NewBaseRepository[User](db.Collection("users")),
    }
}

type UserService struct {
    userRepo *UserRepository
}

// Nika automatically resolves *UserRepository from the DI container
func NewUserService(userRepo *UserRepository) *UserService {
    return &UserService{
        userRepo: userRepo,
    }
}
```

## Registering Providers

Register providers in a module's `Providers()` method. You can pass either an **instance** or a **constructor function**:

```go
func (m *AppModule) Providers() []interface{} {
    return []interface{}{
        // Constructor function — Nika will auto-resolve dependencies
        NewUserService,
        NewUserRepository,

        // Direct instance — registered as-is
        &ConfigService{},
    }
}
```

## How DI Resolution Works

When Nika encounters a constructor function, it:

1. Inspects the function's parameter types using reflection
2. Looks up each parameter type in the DI container
3. Calls the function with the resolved dependencies
4. Registers the return value in the container

```go
// The DI container resolves the chain:
// NewUserRepository needs *mongo.Database  → found in container
// NewUserService needs *UserRepository     → found in container (registered above)
```

## RegisterSingleton

You can also manually register singletons directly in the `App`:

```go
func main() {
    app := nika.NewApp()

    // Register a singleton directly
    app.RegisterSingleton(myInstance)

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Common Package Providers

Nika's common packages follow the `Setup()` pattern, which creates and registers the provider:

```go
// These Setup functions register their instances in the DI container
config.Setup(app, "")
mongodb.Setup(app, mongodb.Config{URI: "mongodb://localhost:27017", Database: "mydb"})
cache.Setup(app, cache.Config{Driver: "redis", URL: "redis://localhost:6379"})
validator.Setup(app)
```

## Summary

| Registration Method | Example | Auto-resolve Deps? |
|---|---|---|
| Constructor function | `NewUserService` | ✅ Yes |
| Direct instance | `&ConfigService{}` | ❌ No |
| `RegisterSingleton` | `app.RegisterSingleton(x)` | ❌ No |
| `Setup()` function | `config.Setup(app, "")` | ✅ Yes (returns value) |

!!! tip "Next Steps"
    Learn about [Modules](modules.md) to organize your providers.
