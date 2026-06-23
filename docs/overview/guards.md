# Guards

> **Coming Soon** — This feature is not yet implemented.

Guards determine whether a given request will be handled by the route handler or not, based on conditions like roles, permissions, or authentication status.

## Planned Design

```go
// Planned API (subject to change)
type Guard interface {
    CanActivate(ctx *gin.Context) bool
}

type AuthGuard struct {
    jwtService *JWTService
}

func (g *AuthGuard) CanActivate(ctx *gin.Context) bool {
    token := ctx.GetHeader("Authorization")
    if token == "" {
        return false
    }
    // Validate token
    claims, err := g.jwtService.Parse(token)
    if err != nil {
        return false
    }
    ctx.Set("user", claims)
    return true
}

type RolesGuard struct {
    AllowedRoles []string
}

func (g *RolesGuard) CanActivate(ctx *gin.Context) bool {
    user, exists := ctx.Get("user")
    if !exists {
        return false
    }
    // Check if user's role is in AllowedRoles
    return true
}
```

## Current Alternative

Until Guards are implemented, use Gin middleware:

```go
func AuthGuard() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        if token == "" {
            c.AbortWithStatusJSON(401, gin.H{"error": "Unauthorized"})
            return
        }

        // Validate token...
        claims, err := validateToken(token)
        if err != nil {
            c.AbortWithStatusJSON(401, gin.H{"error": "Invalid token"})
            return
        }

        c.Set("user", claims)
        c.Next()
    }
}

func RoleGuard(allowedRoles ...string) gin.HandlerFunc {
    return func(c *gin.Context) {
        user, exists := c.Get("user")
        if !exists {
            c.AbortWithStatusJSON(401, gin.H{"error": "Unauthorized"})
            return
        }

        // Check role
        role := user.(UserClaims).Role
        allowed := false
        for _, r := range allowedRoles {
            if r == role {
                allowed = true
                break
            }
        }

        if !allowed {
            c.AbortWithStatusJSON(403, gin.H{"error": "Forbidden"})
            return
        }

        c.Next()
    }
}

// Usage
app.Use(AuthGuard())
```

## Status

| Feature | Status |
|---------|--------|
| Guard interface | ⏳ Planned |
| Global guards | ⏳ Planned |
| Per-route/per-controller guards | ⏳ Planned |
| Role-based access control | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/sajadweb/nika) for guidelines.
