# Global Path Prefix

> **Coming Soon** — Built-in global path prefix is not yet implemented.

A global path prefix allows you to set a common prefix for all routes (e.g., `/api/v1`).

## Current Alternative

Include the prefix in each route tag:

```go
type UserController struct {
    List    func(*gin.Context) `route:"GET:/api/v1/users"`
    Create  func(*gin.Context) `route:"POST:/api/v1/users"`
    FindOne func(*gin.Context) `route:"GET:/api/v1/users/:id"`
}
```

Or use Gin's router group (requires access to the engine):

```go
api := engine.Group("/api/v1")
{
    api.GET("/users", listUsers)
    api.POST("/users", createUser)
}
```

## Status

| Feature | Status |
|---------|--------|
| Global path prefix via App | ⏳ Planned |
| Route groups | ⏳ Planned |
| Version prefix | ⏳ Planned |
