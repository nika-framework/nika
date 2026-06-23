# Serve Static

> **Coming Soon** — Built-in static file serving is not yet implemented.

Use Gin's built-in static file serving:

```go
func main() {
    app := nika.NewApp()

    // Get the underlying Gin engine to serve static files
    // Note: You need to expose the engine or add a method to App

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Using Gin Directly

```go
import "github.com/gin-gonic/gin"

engine := gin.Default()
engine.Static("/assets", "./public")
engine.StaticFS("/static", http.Dir("./static"))
engine.StaticFile("/favicon.ico", "./resources/favicon.ico")
```

## Status

| Feature | Status |
|---------|--------|
| Static file serving | ⏳ Planned |
| Virtual paths | ⏳ Planned |
| SPA support (fallback) | ⏳ Planned |
