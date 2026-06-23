# OpenAPI Introduction

> **Coming Soon** — OpenAPI/Swagger support is not yet implemented.

Nika plans to provide built-in OpenAPI documentation generation.

## Planned Design

```go
// Planned: Swagger decorators via struct tags
type UserController struct {
    // @Summary Get all users
    // @Description Returns a list of users
    // @Tags users
    // @Accept json
    // @Produce json
    // @Success 200 {array} User
    // @Router /users [get]
    List func(*gin.Context) `route:"GET:/users"`
}
```

## Current Alternative

Use [swaggo/swag](https://github.com/swaggo/swag) with Gin:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
go get -u github.com/swaggo/gin-swagger
go get -u github.com/swaggo/files
```

```go
// @title Nika API
// @version 1.0
// @description My Nika API
// @host localhost:3000
// @BasePath /
func main() {
    app := nika.NewApp()

    app.Use(ginSwagger.WrapHandler(swaggerFiles.Handler))
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
