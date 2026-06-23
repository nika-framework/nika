# Middleware

Middleware is a function that is called **before** the route handler. In Nika, middleware uses the underlying Gin middleware system via the `Use()` method.

## Global Middleware

Register middleware that runs for **all** routes using `app.Use()`:

```go
func main() {
    app := nika.NewApp()

    // Register global middleware
    app.Use(Logger())
    app.Use(CORS())
    app.Use(RateLimit())

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Creating Custom Middleware

Middleware in Nika is simply a `gin.HandlerFunc`:

```go
package middleware

import (
    "log"
    "time"
    "github.com/gin-gonic/gin"
)

// Logger logs the request method, path, and duration
func Logger() gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        path := c.Request.URL.Path

        c.Next()

        duration := time.Since(start)
        log.Printf("[%s] %s — %v", c.Request.Method, path, duration)
    }
}

// Auth checks for a valid Authorization header
func Auth() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        if token == "" {
            c.AbortWithStatusJSON(401, gin.H{
                "error": "Authorization header required",
            })
            return
        }
        // Validate token...
        c.Next()
    }
}

// CORS adds Cross-Origin Resource Sharing headers
func CORS() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
        c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

        if c.Request.Method == "OPTIONS" {
            c.AbortWithStatus(204)
            return
        }

        c.Next()
    }
}
```

## Using Third-Party Gin Middleware

Since Nika is built on Gin, you can use any Gin-compatible middleware:

```go
import (
    "github.com/gin-contrib/cors"
    "github.com/gin-contrib/gzip"
    "github.com/gin-contrib/sessions"
    "github.com/gin-contrib/sessions/cookie"
)

func main() {
    app := nika.NewApp()

    // CORS middleware
    app.Use(cors.New(cors.Config{
        AllowOrigins:     []string{"http://localhost:3000"},
        AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
        AllowHeaders:     []string{"Content-Type", "Authorization"},
        AllowCredentials: true,
    }))

    // Gzip compression
    app.Use(gzip.Gzip(gzip.DefaultCompression))

    // Session middleware
    store := cookie.NewStore([]byte("secret"))
    app.Use(sessions.Sessions("mysession", store))

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Middleware Order

Middleware is executed in the order it is registered:

```go
app.Use(Logger())      // 1st
app.Use(Auth())        // 2nd
app.Use(CORS())        // 3rd
// → Request flows: Logger → Auth → CORS → Route Handler
```

!!! tip "Tip"
    For route-specific or group-specific middleware, you can use the `gin.Engine` directly or wait for future Nika group support.

!!! info "Coming Soon"
    Route groups and per-controller/per-route middleware support is planned for future releases.
