# CORS

Cross-Origin Resource Sharing (CORS) allows you to control which domains can access your API.

## Current Approach

Use the built-in `common/cors` wrapper, which abstracts `gin-contrib/cors` and provides validated configuration, sensible defaults, and DI integration.

### Basic Configuration (Allow All Origins)

Useful for local development. By setting `AllowAllOrigins` to `true`, the wrapper automatically applies the necessary defaults.

```go
import (
	"log"
	"github.com/nika-framework/nika/common/cors"
)

func main() {
    app := nika.NewApp()

    _, err := cors.Setup(app, cors.Config{
        AllowAllOrigins: true, // Equivalent to cors.Default()
    })
    if err != nil {
        log.Fatal(err)
    }

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Custom Configuration

Restrict access to specific domains, methods, and headers. If you omit `AllowMethods` or `AllowHeaders`, the module automatically injects standard secure defaults.

```go
_, err := cors.Setup(app, cors.Config{
    AllowOrigins: []string{
        "http://localhost:3000",
        "https://myapp.com",
    },
    AllowMethods: []string{
        "GET",
        "POST",
        "PUT",
        "PATCH",
        "DELETE",
        "OPTIONS",
    },
    AllowHeaders: []string{
        "Origin",
        "Content-Type",
        "Authorization",
        "Accept",
    },
    AllowCredentials: true,
    MaxAge: 12 * time.Hour,
})
if err != nil {
    log.Fatal(err)
}
```

### Dynamic Origin Validation

If you need to validate origins against a database or a complex regex, use `AllowOriginFunc`. 
*(Note: When using `AllowOriginFunc`, you can safely set `AllowCredentials: true` without violating CORS specifications).*

```go
_, err := cors.Setup(app, cors.Config{
    AllowOriginFunc: func(origin string) bool {
        // Example: Allow specific origins dynamically
        allowed := []string{
            "http://localhost:3000",
            "https://myapp.com",
        }
        for _, o := range allowed {
            if origin == o {
                return true
            }
        }
        return false
    },
    AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
    AllowHeaders:     []string{"Content-Type", "Authorization"},
    AllowCredentials: true,
})
if err != nil {
    log.Fatal(err)
}
```

> **Security Note:** The `cors.Setup` function includes built-in validation. For example, it will return an error if you try to set both `AllowAllOrigins: true` and `AllowCredentials: true` simultaneously, preventing your application from crashing at runtime.

## Status

| Feature | Status |
|---------|--------|
| Built-in CORS wrapper (`common/cors`) | ✅ Available |
