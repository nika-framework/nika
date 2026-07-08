# OpenAPI Introduction

Nika plans to provide built-in OpenAPI documentation generation.

## Planned Design

```go
//Swagger decorators via struct tags
type UserController struct {
   
    List func(*gin.Context) `route:"GET:/users"`
}
func NewUserController(service *UserService) *UserController {
     ctrl := &UserController{service: service}
     ctrl.List=ListHandler
    return ctrl
}

// @Summary Get all users
// @Description Returns a list of users
// @Tags users
// @Accept json
// @Produce json
// @Success 200 {array} User
// @Router /users [get]
func (ctrl *UserController) ListHandler(c *gin.Context) {
    users := ctrl.service.FindAll()
    c.JSON(http.StatusOK, users)
}
```

## Current Alternative

Use [swaggo/swag](https://github.com/swaggo/swag) with Gin:

```bash

nika swagger init


nika swagger init --dir ./cmd --output ./api/docs


nika swagger init --parseDependency --parseInternal --parseDepth 200

nika run --watch
```

```go
import (
    _ "NikaSamole/docs"
    "github.com/nika-framework/nika/common/swagger"
)
// @title Nika API
// @version 1.0
// @description My Nika API
// @host localhost:3000
// @BasePath /
func main() {
    app := nika.NewApp()

    swagger.Setup(app,swagger.Config{
        Path:"swagger/*any"
    })
    // ...
}
```

## Status

| Feature | Status |
|---------|--------|
| Auto-generated Swagger docs | ⏳ Planned |
| Decorator-based API docs | ⏳ Planned |
| API response examples | ⏳ Planned |

!!! info "Want to contribute?"
    OpenAPI support is open for contribution.
