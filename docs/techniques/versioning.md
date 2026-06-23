# Versioning

> **Coming Soon** — API versioning is not yet implemented.

Versioning allows you to manage multiple versions of your API simultaneously.

## Planned Design

```go
// Planned API (subject to change)
// Route tag with version
type UserController struct {
    ListV1  func(*gin.Context) `route:"GET:/v1/users"`
    ListV2  func(*gin.Context) `route:"GET:/v2/users"`
}

// Or automatic versioning
type UserController struct {
    List func(*gin.Context) `route:"GET:/users" version:"v1"`
}
```

## Current Alternative

Use manual path prefixes:

```go
type UserController struct {
    ListV1 func(*gin.Context) `route:"GET:/v1/users"`
    ListV2 func(*gin.Context) `route:"GET:/v2/users"`
}
```

## Status

| Feature | Status |
|---------|--------|
| URI versioning (`/v1/`, `/v2/`) | ⏳ Planned |
| Header versioning | ⏳ Planned |
| Media type versioning | ⏳ Planned |
| Version deprecation | ⏳ Planned |
