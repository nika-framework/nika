# Interceptors

> **Coming Soon** — This feature is not yet implemented.

Interceptors provide a way to bind extra logic before and after method execution. They are useful for logging, response transformation, caching, and error handling.

## Planned Design

```go
// Planned API (subject to change)
type Interceptor interface {
    Intercept(ctx *gin.Context, handler gin.HandlerFunc) gin.HandlerFunc
}

// Logging interceptor
type LoggingInterceptor struct{}

func (i *LoggingInterceptor) Intercept(ctx *gin.Context, handler gin.HandlerFunc) gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        log.Printf("→ Request: %s %s", c.Request.Method, c.Request.URL.Path)

        handler(c)

        log.Printf("← Response: %d (%v)", c.Writer.Status(), time.Since(start))
    }
}

// Response transformation interceptor
type TransformInterceptor struct{}

func (i *TransformInterceptor) Intercept(ctx *gin.Context, handler gin.HandlerFunc) gin.HandlerFunc {
    return func(c *gin.Context) {
        // Wrap response writer
        // Call handler
        // Transform response
        handler(c)
    }
}
```

## Current Alternative

Use Gin middleware as a workaround:

```go
// Response wrapper middleware
func ResponseWrapper() gin.HandlerFunc {
    return func(c *gin.Context) {
        // Before handler
        start := time.Now()

        c.Next()

        // After handler — you can modify the response here
        duration := time.Since(start)
        c.Header("X-Response-Time", duration.String())
    }
}

// Usage
app.Use(ResponseWrapper())
```

## Use Cases

| Use Case | Description |
|----------|-------------|
| **Logging** | Log requests and responses |
| **Response transformation** | Wrap all responses in a standard format |
| **Caching** | Cache responses |
| **Error handling** | Catch and transform errors |
| **Performance monitoring** | Measure handler execution time |

## Status

| Feature | Status |
|---------|--------|
| Interceptor interface | ⏳ Planned |
| Global interceptors | ⏳ Planned |
| Per-route interceptors | ⏳ Planned |
| Response mapping | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/sajadweb/nika) for guidelines.
