# Session

> **Coming Soon** — Built-in session support is not yet implemented.

Use Gin-compatible session middleware from `gin-contrib/sessions`.

## Current Approach

```bash
go get github.com/gin-contrib/sessions
go get github.com/gin-contrib/sessions/cookie
```

```go
import (
    "github.com/gin-contrib/sessions"
    "github.com/gin-contrib/sessions/cookie"
)

func main() {
    app := nika.NewApp()

    // Cookie-based sessions
    store := cookie.NewStore([]byte("your-secret-key"))
    app.Use(sessions.Sessions("nikasession", store))

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Using Sessions in Controllers

```go
func (ctrl *AuthController) Login(c *gin.Context) {
    session := sessions.Default(c)

    // Set session values
    session.Set("user_id", userID)
    session.Set("role", "admin")
    session.Save()

    response.OkByMsg(c, "Logged in successfully")
}

func (ctrl *AuthController) Profile(c *gin.Context) {
    session := sessions.Default(c)

    // Get session values
    userID := session.Get("user_id")
    if userID == nil {
        c.JSON(401, gin.H{"error": "Not authenticated"})
        return
    }

    response.Ok(c, gin.H{"user_id": userID})
}

func (ctrl *AuthController) Logout(c *gin.Context) {
    session := sessions.Default(c)
    session.Clear()
    session.Save()

    response.OkByMsg(c, "Logged out successfully")
}
```

### Redis Session Store

```bash
go get github.com/gin-contrib/sessions/redis
```

```go
import "github.com/gin-contrib/sessions/redis"

store, _ := redis.NewStore(10, "tcp", "localhost:6379", "", []byte("secret"))
app.Use(sessions.Sessions("nikasession", store))
```

## Status

| Feature | Status |
|---------|--------|
| Cookie session store | ✅ Available via gin-contrib |
| Redis session store | ✅ Available via gin-contrib |
| Memcached session store | ✅ Available via gin-contrib |
| Built-in session support | ⏳ Planned |
