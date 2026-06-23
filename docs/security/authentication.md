# Authentication

> **Coming Soon** — Built-in authentication is not yet implemented.

Authentication is the process of verifying the identity of a user. This section covers how to implement authentication in Nika applications.

## Current Approach (JWT)

Use a JWT library with Gin middleware:

```bash
go get github.com/golang-jwt/jwt/v5
```

```go
package middleware

import (
    "net/http"
    "strings"
    "github.com/gin-gonic/gin"
    "github.com/golang-jwt/jwt/v5"
)

type JWTService struct {
    secret []byte
}

func NewJWTService(secret string) *JWTService {
    return &JWTService{secret: []byte(secret)}
}

func (s *JWTService) GenerateToken(userID string, role string) (string, error) {
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
        "user_id": userID,
        "role":    role,
        "exp":     time.Now().Add(24 * time.Hour).Unix(),
    })

    return token.SignedString(s.secret)
}

func (s *JWTService) ValidateToken(tokenString string) (jwt.MapClaims, error) {
    token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
        return s.secret, nil
    })
    if err != nil {
        return nil, err
    }
    return token.Claims.(jwt.MapClaims), nil
}

// Auth middleware
func AuthMiddleware(jwtService *JWTService) gin.HandlerFunc {
    return func(c *gin.Context) {
        authHeader := c.GetHeader("Authorization")
        if authHeader == "" {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
                "error": "Authorization header required",
            })
            return
        }

        tokenString := strings.TrimPrefix(authHeader, "Bearer ")
        claims, err := jwtService.ValidateToken(tokenString)
        if err != nil {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
                "error": "Invalid token",
            })
            return
        }

        c.Set("user_id", claims["user_id"])
        c.Set("role", claims["role"])
        c.Next()
    }
}
```

### Usage

```go
func main() {
    app := nika.NewApp()

    jwtService := NewJWTService("your-secret-key")

    // Protected routes
    app.Use(AuthMiddleware(jwtService))

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Status

| Feature | Status |
|---------|--------|
| JWT authentication | ⏳ Planned (use middleware workaround) |
| API Key authentication | ⏳ Planned |
| OAuth2 / OIDC | ⏳ Planned |
| Session-based authentication | ⏳ Planned |
| Multi-factor authentication | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/sajadweb/nika).
