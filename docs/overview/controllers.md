# Controllers

Controllers are responsible for handling incoming **requests** and returning **responses** to the client. In Nika, controllers use a **struct-tag routing** approach where routes are defined directly on exported function fields.

## Route Definition

Routes are defined using the `route` struct tag in the format `METHOD:path`:

```go
type UserController struct {
    service *UserService
}

func NewUserController(service *UserService) *UserController {
    return &UserController{service: service}
}
```

### Route Handler Functions

Each exported function field in the controller struct can be tagged as a route:

```go
func NewUserController(service *UserService) *UserController {
    ctrl := &UserController{service: service}

    ctrl.List = func(c *gin.Context) {
        users := ctrl.service.FindAll()
        c.JSON(http.StatusOK, users)
    }

    ctrl.Create = func(c *gin.Context) {
        // handler logic
    }

    ctrl.FindOne = func(c *gin.Context) {
        id := c.Param("id")
        user, err := ctrl.service.FindByID(id)
        if err != nil {
            c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
            return
        }
        c.JSON(http.StatusOK, user)
    }

    return ctrl
}
```

### Supported HTTP Methods

| Method | Tag Example | Description |
|--------|-----------|-------------|
| `GET` | `route:"GET:/users"` | Retrieve resources |
| `POST` | `route:"POST:/users"` | Create a resource |
| `PUT` | `route:"PUT:/users/:id"` | Replace a resource |
| `PATCH` | `route:"PATCH:/users/:id"` | Partially update a resource |
| `DELETE` | `route:"DELETE:/users/:id"` | Delete a resource |
| `OPTIONS` | `route:"OPTIONS:/users"` | CORS preflight |

## Full Example

Here is a complete controller with multiple routes:

```go
package src

import (
    "net/http"
    "github.com/gin-gonic/gin"
)

type UserController struct {
    service *UserService
    List    func(*gin.Context) `route:"GET:/users"`
    Create  func(*gin.Context) `route:"POST:/users"`
    FindOne func(*gin.Context) `route:"GET:/users/:id"`
    Update  func(*gin.Context) `route:"PUT:/users/:id"`
    Delete  func(*gin.Context) `route:"DELETE:/users/:id"`
}

func NewUserController(service *UserService) *UserController {
    ctrl := &UserController{service: service}

    ctrl.List = func(c *gin.Context) {
        users := ctrl.service.FindAll()
        c.JSON(http.StatusOK, users)
    }

    ctrl.Create = func(c *gin.Context) {
        var dto CreateUserDTO
        if !validator.BindAndValidate(c, &dto) {
            return
        }
        user := ctrl.service.Create(dto)
        response.Create(c, user)
    }

    ctrl.FindOne = func(c *gin.Context) {
        id := c.Param("id")
        user := ctrl.service.FindOne(id)
        if user == nil {
            response.NotFoundRequest(c, "USER_NOT_FOUND", "User not found", nil)
            return
        }
        response.Ok(c, user)
    }

    ctrl.Update = func(c *gin.Context) {
        id := c.Param("id")
        var dto UpdateUserDTO
        if !validator.BindAndValidate(c, &dto) {
            return
        }
        ctrl.service.Update(id, dto)
        response.OkByMsg(c, "User updated successfully")
    }

    ctrl.Delete = func(c *gin.Context) {
        id := c.Param("id")
        ctrl.service.Delete(id)
        response.OkByMsg(c, "User deleted successfully")
    }

    return ctrl
}
```

## Dependency Injection in Controllers

Controllers are automatically resolved by Nika's DI container. When you register a controller as a **constructor function**, Nika resolves all dependencies from the container:

```go
// Constructor returns *UserController — Nika auto-resolves *UserService
func NewUserController(service *UserService) *UserController {
    // ...
}

// Register in module
func (m *AppModule) Controllers() []interface{} {
    return []interface{}{
        NewUserController, // Pass the constructor, not an instance
    }
}
```

## Registration

Register your controllers in a module's `Controllers()` method:

```go
func (m *AppModule) Controllers() []interface{} {
    return []interface{}{
        NewUserController,
        NewAuthController,
    }
}
```

When the application starts, Nika will:

1. Call `NewUserController` and resolve `*UserService` from the DI container
2. Scan the struct fields for `route` tags
3. Register each tagged function as a Gin route

```
✅ Registered: GET /users -> List
✅ Registered: POST /users -> Create
✅ Registered: GET /users/:id -> FindOne
✅ Registered: PUT /users/:id -> Update
✅ Registered: DELETE /users/:id -> Delete
```

!!! tip "Note"
    Route handler fields **must be exported** (start with an uppercase letter) and must be of type `func(*gin.Context)`.
