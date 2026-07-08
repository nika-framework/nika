# Logging

> **Coming Soon** — A dedicated logging package is not yet implemented.

Nika currently relies on Gin's default logger for HTTP request logging.

## Current Logging

### Gin Default Logger

Gin's default middleware logs all HTTP requests:

```go
app := nika.NewApp()
// Gin's default logger is included automatically
```

Output:
```
[GIN] 2024/01/15 - 10:30:00 | 200 | 12.3ms | 127.0.0.1 | GET "/users"
```

### Custom Logger Middleware

```go
import (
    "log"
    "time"
    "github.com/gin-gonic/gin"
)

func CustomLogger() gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        path := c.Request.URL.Path
        query := c.Request.URL.RawQuery

        c.Next()

        duration := time.Since(start)
        log.Printf(
            "[%s] %s %s | %d | %v | %s",
            c.Request.Method,
            path,
            query,
            c.Writer.Status(),
            duration,
            c.ClientIP(),
        )
    }
}

// Usage
app.Use(CustomLogger())
```

### Disable Gin's Default Logger

```go
import "github.com/gin-gonic/gin"

func main() {
    gin.SetMode(gin.ReleaseMode) // Disables debug logging
    app := nika.NewApp()
    // ...
}
```

## Planned Features

```go
// Planned API (subject to change)
import "github.com/nika-framework/nika/common/logger"

func Setup(app *nika.App, cfg Config) *Logger {
    // Create structured logger
}

logger.Info("User created", logger.String("email", email))
logger.Error("Failed to connect", logger.Err(err))
```

## Status

| Feature | Status |
|---------|--------|
| HTTP request logging (Gin default) | ✅ Available |
| Structured logging | ⏳ Planned |
| Log levels (debug, info, warn, error) | ⏳ Planned |
| Log file rotation | ⏳ Planned |
| JSON log format | ⏳ Planned |
