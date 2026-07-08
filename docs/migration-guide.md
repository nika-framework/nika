# Migration Guide

This guide helps you migrate from other Go frameworks to Nika.

## From Plain Gin

If you're currently using Gin directly, migrating to Nika is straightforward:

### Before (Plain Gin)

```go
package main

import "github.com/gin-gonic/gin"

func main() {
    r := gin.Default()

    r.GET("/users", func(c *gin.Context) {
        c.JSON(200, gin.H{"users": []string{"Alice", "Bob"}})
    })

    r.Run(":3000")
}
```

### After (Nika)

```go
package main

import (
    "net/http"
    "github.com/gin-gonic/gin"
    "github.com/nika-framework/nika"
)

// 1. Define your module
type AppModule struct{}

func NewAppModule() *AppModule { return &AppModule{} }

func (m *AppModule) Controllers() []interface{} { return []interface{}{NewUserController} }
func (m *AppModule) Providers()   []interface{} { return []interface{}{} }
func (m *AppModule) Imports()     []nika.Module { return []nika.Module{} }

// 2. Define your controller
type UserController struct {
    List func(*gin.Context) `route:"GET:/users"`
}

func NewUserController() *UserController {
    ctrl := &UserController{}
    ctrl.List = func(c *gin.Context) {
        c.JSON(http.StatusOK, gin.H{"users": []string{"Alice", "Bob"}})
    }
    return ctrl
}

// 3. Bootstrap
func main() {
    app := nika.NewApp()
    app.LoadModule(NewAppModule())
    app.Listen(":3000")
}
```

## Key Differences

| Feature | Plain Gin | Nika |
|---------|-----------|------|
| Routing | `r.GET("/path", handler)` | `route:"GET:/path"` struct tag |
| Dependency Injection | Manual | Built-in DI container |
| Module organization | Manual | Module interface |
| Middleware | `r.Use(middleware)` | `app.Use(middleware)` |
| Response format | Manual | `common/response` helpers |
| Validation | Manual | `common/validator` helpers |



## Compatibility

Nika is built on Gin, so **all Gin-compatible middleware, handlers, and tools work seamlessly** with Nika.
