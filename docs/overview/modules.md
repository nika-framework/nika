# Modules

A **Module** is a class annotated with a `Module` decorator. Modules encapsulate a closely related set of capabilities: controllers, providers, and imported modules.

## Module Interface

In Nika, a Module is any struct that implements the `nika.Module` interface:

```go
type Module interface {
    Controllers() []interface{}
    Providers()   []interface{}
    Imports()     []Module
}
```

- **Controllers()** — Returns the controllers (or their constructors) for this module
- **Providers()** — Returns the providers (or their constructors) for this module
- **Imports()** — Returns sub-modules to load recursively

## Creating a Module

```go
package src

import "github.com/sajadweb/nika"

type UsersModule struct{}

func NewUsersModule() *UsersModule {
    return &UsersModule{}
}

func (m *UsersModule) Controllers() []interface{} {
    return []interface{}{
        NewUserController,
    }
}

func (m *UsersModule) Providers() []interface{} {
    return []interface{}{
        NewUserService,
        NewUserRepository,
    }
}

func (m *UsersModule) Imports() []nika.Module {
    return []nika.Module{}
}
```

## Root Module

The **root module** is the entry point of your application. It imports all feature modules:

```go
package src

import "github.com/sajadweb/nika"

type AppModule struct{}

func NewAppModule() *AppModule {
    return &AppModule{}
}

func (m *AppModule) Controllers() []interface{} {
    return []interface{}{
        NewHealthController,
    }
}

func (m *AppModule) Providers() []interface{} {
    return []interface{}{}
}

func (m *AppModule) Imports() []nika.Module {
    return []nika.Module{
        NewUsersModule(),
        NewAuthModule(),
        NewConfigModule(),
    }
}
```

## Feature Modules

Organize your application into feature modules. Each module is self-contained:

```go
// Auth module
type AuthModule struct{}

func NewAuthModule() *AuthModule {
    return &AuthModule{}
}

func (m *AuthModule) Controllers() []interface{} {
    return []interface{}{
        NewAuthController,
    }
}

func (m *AuthModule) Providers() []interface{} {
    return []interface{}{
        NewAuthService,
        NewTokenService,
    }
}

func (m *AuthModule) Imports() []nika.Module {
    return []nika.Module{}
}
```

## Module Loading Order

When `app.LoadModule(rootModule)` is called, Nika processes modules in the following order:

```
1. Recursively load all Imports (sub-modules)
2. Register all Providers into the DI container
3. Resolve and register all Controllers
```

This means providers from imported modules are available before the parent module's providers and controllers are resolved.

## Directory Structure

A recommended project structure:

```
my-app/
├── main.go
├── src/
│   ├── app_module.go          # Root module
│   ├── modules/
│   │   ├── users/
│   │   │   ├── users_module.go
│   │   │   ├── user_controller.go
│   │   │   ├── user_service.go
│   │   │   └── user_repository.go
│   │   ├── auth/
│   │   │   ├── auth_module.go
│   │   │   ├── auth_controller.go
│   │   │   └── auth_service.go
│   │   └── config/
│   │       └── config_module.go
│   └── dto/
│       └── user_dto.go
├── go.mod
└── go.sum
```

## Shared Providers

Since the DI container is **global**, providers registered in any module are available to all other modules:

```go
// In AppModule's Providers
func (m *AppModule) Providers() []interface{} {
    return []interface{}{
        NewDatabaseService, // Available to ALL modules
    }
}

// UserService in UsersModule can access DatabaseService
func NewUserService(db *DatabaseService) *UserService {
    return &UserService{db: db}
}
```

!!! warning "Important"
    Module import order matters for provider resolution. If module A depends on a provider from module B, module B must be imported **before** module A (or listed first in the `Imports()` slice).
