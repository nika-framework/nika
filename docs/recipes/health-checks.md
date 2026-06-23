# Health Checks

> **Coming Soon** — Built-in health checks are not yet implemented.

Health checks provide a way to monitor the status of your application and its dependencies (database, cache, external services).

## Current Alternative

Create a simple health check endpoint:

```go
type HealthController struct {
    Check func(*gin.Context) `route:"GET:/health"`
}

func NewHealthController(db *mongodb.MongoDB, cache *cache.Cache) *HealthController {
    ctrl := &HealthController{}

    ctrl.Check = func(c *gin.Context) {
        status := gin.H{
            "status":  "ok",
            "app":     "Nika",
            "version": "1.0.0",
            "services": gin.H{},
        }

        // Check MongoDB
        if err := db.Client.Ping(c, nil); err != nil {
            status["status"] = "degraded"
            status["services"].(gin.H)["mongodb"] = "unhealthy"
        } else {
            status["services"].(gin.H)["mongodb"] = "healthy"
        }

        // Check Cache
        if err := cache.Ping(c); err != nil {
            status["status"] = "degraded"
            status["services"].(gin.H)["cache"] = "unhealthy"
        } else {
            status["services"].(gin.H)["cache"] = "healthy"
        }

        code := http.StatusOK
        if status["status"] == "degraded" {
            code = http.StatusServiceUnavailable
        }

        c.JSON(code, status)
    }

    return ctrl
}
```

## Status

| Feature | Status |
|---------|--------|
| Health check endpoint | ⏳ Planned |
| Dependency health monitoring | ⏳ Planned |
| Readiness/Liveness probes | ⏳ Planned |
| Health check indicators | ⏳ Planned |
