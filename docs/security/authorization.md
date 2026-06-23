# Authorization

> **Coming Soon** — Built-in authorization is not yet implemented.

Authorization determines what an authenticated user is allowed to do. This includes role-based access control (RBAC) and attribute-based access control (ABAC).

## Current Approach

Use Gin middleware for role-based authorization:

```go
package middleware

import (
    "net/http"
    "github.com/gin-gonic/gin"
)

// RequireRole checks if the authenticated user has the required role
func RequireRole(roles ...string) gin.HandlerFunc {
    return func(c *gin.Context) {
        userRole, exists := c.Get("role")
        if !exists {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
                "error": "Not authenticated",
            })
            return
        }

        authorized := false
        for _, role := range roles {
            if userRole.(string) == role {
                authorized = true
                break
            }
        }

        if !authorized {
            c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
                "error": "Insufficient permissions",
            })
            return
        }

        c.Next()
    }
}
```

### Usage

```go
func (ctrl *AdminController) Users(c *gin.Context) {
    // Only accessible by admin role
    users := ctrl.service.FindAll()
    response.Ok(c, users)
}

// In your route setup:
// app.Use(AuthMiddleware(jwtService))
// app.Use(RequireRole("admin"))
```

## Planned Design

```go
// Planned: Role guard
type RolesGuard struct {
    Roles []string
}

func (g *RolesGuard) CanActivate(ctx *gin.Context) bool {
    role, exists := ctx.Get("role")
    if !exists {
        return false
    }
    for _, r := range g.Roles {
        if role == r {
            return true
        }
    }
    return false
}
```

## Status

| Feature | Status |
|---------|--------|
| Role-based access control (RBAC) | ⏳ Planned |
| Attribute-based access control (ABAC) | ⏳ Planned |
| Permission decorators | ⏳ Planned |
| Policy-based authorization | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution.
