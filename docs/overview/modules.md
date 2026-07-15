# Modules

A **Module** is a class annotated with a `Module` decorator. Modules encapsulate a closely related set of capabilities: controllers, providers, and imported modules.

## Module Interface

In Nika, a Module is any struct that implements the `nika.Module` interface:

```go
type Module interface {
    Controllers() []interface{}
    Providers()   []interface{}
    Imports()     []Module
    Exports()     []interface{}
}
```

- **Controllers()** — Returns the controllers (or their constructors) for this module
- **Providers()** — Returns the providers (or their constructors) for this module
- **Imports()** — Returns sub-modules to load recursively
- **Exports()** — Returns providers that modules importing this module may inject

## Creating a Module

```go
package src

import "github.com/nika-framework/nika"

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

func (m *UsersModule) Exports() []interface{} {
    return []interface{}{}
}
```

## Root Module

The **root module** is the entry point of your application. It imports all feature modules:

```go
package src

import "github.com/nika-framework/nika"

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

func (m *AppModule) Exports() []interface{} {
    return []interface{}{}
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

func (m *AuthModule) Exports() []interface{} {
    return []interface{}{}
}
```

## Module Loading Order

When `app.LoadModule(rootModule)` is called, Nika processes modules in the following order:

```
1. Recursively load all Imports (sub-modules)
2. Register all Providers into the DI container
3. Resolve and register all Controllers
```

This means exported providers from imported modules are available before the parent module's providers and controllers are resolved.

## Directory Structure

A recommended project structure:

```
my-app/
├── main.go
├── src/
│   ├── app_module.go          # Root module
│   ├── users/
│   │   ├── users_module.go
│   │   ├── user_controller.go
│   │   ├── user_service.go
│   │   └── user_repository.go
│   ├── auth/
│   │   ├── auth_module.go
│   │   ├── auth_controller.go
│   └── └── auth_service.go
│   
├── go.mod
└── go.sum
```

## Shared Providers

A module can expose selected providers to modules that import it. Private providers remain available only to their own module:

```go
// RoleModule
func (m *RoleModule) Providers() []interface{} {
    return []interface{}{
        NewRoleRepository,
        NewRoleService,
    }
}

func (m *RoleModule) Exports() []interface{} {
    return []interface{}{NewRoleService}
}

// UserModule imports RoleModule and can inject *RoleService.
// *RoleRepository remains private to RoleModule.
```

App-level singletons registered with `app.RegisterSingleton()` remain available to every module.
