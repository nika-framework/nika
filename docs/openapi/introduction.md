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

    swagger.Setup(app, &swagger.Config{
        Path:    "/docs",
        Enabled: swagger.Enable(true),
    })
    // ...
}
```

`Path` is the base path, so the UI is at `/docs` — `/docs` and `/docs/` both
redirect to `/docs/index.html`, and gin's wildcard form (`/docs/*any`) is
accepted too. Omit `Path` and the docs land on `/swagger`.

!!! warning "The docs are off in release mode"
    `nika.NewApp()` defaults to release mode, and `Setup` skips mounting there so
    a production build does not publish the whole API surface — which means
    `/docs` answers `ROUTE_NOT_FOUND` until you say otherwise. `Setup` prints a
    warning when it skips, and returns `false`. Turn the docs on with
    `Enabled: swagger.Enable(true)`, or run in debug mode (`GIN_MODE=debug`, or
    `nika.NewApp(nika.Config{Mode: "debug"})`). Better still, tie it to your
    environment so production stays quiet:

    ```go
    cfg := config.Setup(app, ".env")
    swagger.Setup(app, &swagger.Config{
        Path:    "/docs",
        Enabled: swagger.Enable(cfg.GetString("DEBUG") == "development"),
    })
    ```

    If you do expose the docs in production, put them behind `Guards` — see
    [Hardening](../security/hardening.md#api-documentation).

## Status

| Feature | Status |
|---------|--------|
| Auto-generated Swagger docs | ⏳ Planned |
| Decorator-based API docs | ⏳ Planned |
| API response examples | ⏳ Planned |

!!! info "Want to contribute?"
    OpenAPI support is open for contribution.
