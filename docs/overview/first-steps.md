# First Steps

In this chapter, you will learn how to build a basic Nika application from scratch. We'll create a simple application that returns a list of users.

## Installation

First, initialize a new Go module and install Nika:

```bash
go install github.com/sajadweb/nika-cli@latest

nika new <app-name>

cd <app-name>

go mod tidy

go run .
```

## Creating a Module

Modules are the building blocks of a Nika application. Each module encapsulates a specific domain or feature.

```go
package src

import (
    "github.com/gin-gonic/gin"
    "github.com/sajadweb/nika"
)

// AppModule is the root module of the application
type AppModule struct{}

func NewAppModule() *AppModule {
    return &AppModule{}
}

func (m *AppModule) Controllers() []interface{} {
    return []interface{}{
        NewUserController,
    }
}

func (m *AppModule) Providers() []interface{} {
    return []interface{}{
        NewUserService,
    }
}

func (m *AppModule) Imports() []nika.Module {
    return []nika.Module{}
}
```

## Creating a Service (Provider)

Providers are the core building blocks that contain business logic. They are automatically injected via Nika's DI container.

```go
package src

type UserService struct{}

func NewUserService() *UserService {
    return &UserService{}
}

func (s *UserService) FindAll() []string {
    return []string{"Alice", "Bob", "Charlie"}
}
```

## Creating a Controller

Controllers handle incoming HTTP requests and return responses. In Nika, routes are defined using struct tags.

```go
package src

import (
    "net/http"
    "github.com/gin-gonic/gin"
)

type UserController struct {
    service *UserService
}

func NewUserController(service *UserService) *UserController {
    return &UserController{service: service}
}

var ListUsers = func(c *gin.Context) {
    // Your handler logic here
    c.JSON(http.StatusOK, gin.H{"users": []string{"Alice", "Bob"}})
}
```

## Bootstrap the Application

Finally, create the `main.go` file to bootstrap your Nika application:

```go
package main

import (
    "fmt"
    "my-nika-app/src"
    "github.com/sajadweb/nika"
)

func main() {
    app := nika.NewApp()

    rootModule := src.NewAppModule()
    app.LoadModule(rootModule)

    fmt.Printf("🚀 Nika is running on http://localhost:3001\n")
    app.Listen(":3001")
}
```

## Run the Application

```bash
go run main.go
```

You should see output similar to:

```
✅ Registered: GET /users -> List
🚀 Nika is running on http://localhost:3001
```

## Summary

In this chapter you built a basic Nika application with:

- A **Module** that organizes controllers and providers
- A **Provider (Service)** that contains business logic
- A **Controller** that handles HTTP requests
- A **Main entry point** that bootstraps everything

!!! tip "Next Steps"
    Learn more about [Controllers](controllers.md) and [Modules](modules.md) in the next chapters.
