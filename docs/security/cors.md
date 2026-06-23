# CORS

Cross-Origin Resource Sharing (CORS) allows you to control which domains can access your API.

## Current Approach

Use `gin-contrib/cors`:

```bash
go get github.com/gin-contrib/cors
```

### Basic Configuration

```go
import "github.com/gin-contrib/cors"

func main() {
    app := nika.NewApp()

    // Allow all origins (development only)
    app.Use(cors.Default())

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Custom Configuration

```go
app.Use(cors.New(cors.Config{
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
}))
```

### Dynamic Origin

```go
app.Use(cors.New(cors.Config{
    AllowOriginFunc: func(origin string) bool {
        // Allow specific origins
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
}))
```

## Status

| Feature | Status |
|---------|--------|
| CORS middleware | ✅ Available via gin-contrib |
| Built-in CORS support | ⏳ Planned |
