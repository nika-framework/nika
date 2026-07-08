# Exception Filters

> **Coming Soon** — This feature is not yet implemented.

Exception filters provide a centralized mechanism for handling errors and uncaught exceptions across your application. They allow you to catch and transform errors into consistent, user-friendly HTTP responses.

## Planned Design

```go
// Planned API design (subject to change)
type ExceptionFilter interface {
    Catch(err error, c *gin.Context)
}

func NewHttpExceptionFilter() *HttpExceptionFilter {
    return &HttpExceptionFilter{}
}

func (f *HttpExceptionFilter) Catch(err error, c *gin.Context) {
    // Transform error into standardized response
    response.JSONError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}
```

## Current Alternative

Until exception filters are implemented, you can use Gin's recovery middleware and manual error handling:

```go
func main() {
    app := nika.NewApp()

    // Gin's built-in recovery middleware
    app.Use(gin.Recovery())

    // Custom error handling middleware
    app.Use(func(c *gin.Context) {
        c.Next()

        // Check for errors set during handler execution
        if len(c.Errors) > 0 {
            err := c.Errors.Last()
            response.JSONError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
        }
    })

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Status

| Feature | Status |
|---------|--------|
| Global exception filters | ⏳ Planned |
| Per-route exception filters | ⏳ Planned |
| Custom exception classes | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/nika-framework/nika) for guidelines.
