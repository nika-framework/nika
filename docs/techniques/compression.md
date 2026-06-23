# Compression

> **Coming Soon** — Built-in compression support is not yet implemented.

Use Gin-compatible compression middleware from `gin-contrib/gzip`.

## Current Approach

```bash
go get github.com/gin-contrib/gzip
```

```go
import "github.com/gin-contrib/gzip"

func main() {
    app := nika.NewApp()

    // Enable gzip compression
    app.Use(gzip.Gzip(gzip.DefaultCompression))

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Status

| Feature | Status |
|---------|--------|
| Gzip compression | ✅ Available via gin-contrib |
| Brotli compression | ⏳ Planned |
| Built-in support | ⏳ Planned |
