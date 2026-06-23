# Performance (Gin)

> **Coming Soon** — Alternative HTTP engine support is not yet implemented.

This section will cover performance optimization and alternative HTTP engines for Nika.

## Current Engine

Nika is built on **Gin**, which is already one of the fastest HTTP frameworks in Go:

```
BenchmarkGin          30000   48265 ns/op   0 B/op   0 allocs/op
```

## Future Engines

| Engine | Status | Description |
|--------|--------|-------------|
| Gin | ✅ Current | Default HTTP engine |
| Fiber | ⏳ Planned | Express-like API, fasthttp-based |
| Chi | ⏳ Planned | Lightweight, composable router |
| Echo | ⏳ Planned | Minimalist framework |
| net/http | ⏳ Planned | Go standard library |

## Current Optimizations

### Disable Debug Mode

```go
import "github.com/gin-gonic/gin"

func main() {
    gin.SetMode(gin.ReleaseMode)
    app := nika.NewApp()
    // ...
}
```

### Enable Gzip Compression

```go
import "github.com/gin-contrib/gzip"

app.Use(gzip.Gzip(gzip.DefaultCompression))
```

### Performance Tips

1. **Use connection pooling** for database and cache connections
2. **Minimize allocations** in hot paths
3. **Use streaming** for large responses
4. **Enable Keep-Alive** connections
5. **Use context timeouts** for external calls

## Status

| Feature | Status |
|---------|--------|
| Gin engine | ✅ Implemented |
| Engine abstraction layer | ⏳ Planned |
| Fiber adapter | ⏳ Planned |
| Performance benchmarks | ⏳ Planned |
