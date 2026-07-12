# Guards

Guards determine whether a given request will be handled by the route handler or not, based on conditions like roles, permissions, or authentication status.

Nika implements guards as lightweight **function-based middleware** that you register by name and attach to controller handlers declaratively using struct tags. Guards run **before** the route handler and may abort the request.

## How It Works

A guard is a function that accepts a list of string arguments (parsed from the tag) and returns a `gin.HandlerFunc`:

```go
type GuardFunc func(args []string) gin.HandlerFunc
```

1. You register a guard with `app.AddGuard(name, fn)`.
2. You attach one or more guards to a controller method via the `guard` struct tag.
3. On startup, `RegisterControllers` parses the tag, resolves each named guard, builds the middlewares (passing the args), and chains them **before** the handler.

## Registering a Guard

```go
package src

import (
    "github.com/gin-gonic/gin"
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/response"
)

func RegisterGuards(app *nika.App) {
    app.AddGuard("auth", func(args []string) gin.HandlerFunc {
        return func(c *gin.Context) {
            token := c.GetHeader("Authorization")
            if token == "" {
                response.UnauthorizedRequest(c, "UNAUTHORIZED", "missing token")
                return
            }
            // Validate token and store claims in context...
            c.Set("user", "example-user")
            c.Next()
        }
    })

    app.AddGuard("role", func(args []string) gin.HandlerFunc {
        // args contains the roles parsed from the tag, e.g. ["admin", "editor"]
        return func(c *gin.Context) {
            user, exists := c.Get("user")
            if !exists {
                response.UnauthorizedRequest(c, "UNAUTHORIZED", "no user in context")
                return
            }

            role := user.(UserClaims).Role
            for _, allowed := range args {
                if allowed == role {
                    c.Next()
                    return
                }
            }
            response.ForbiddenRequest(c, "FORBIDDEN", "insufficient role")
        }
    })
}
```

## Attaching Guards to a Controller

Use the `guard` tag. Guards use the `Name(arg1, arg2, ...)` syntax. Multiple guards can be chained, and they run in the order they appear.

```go
type UserController struct {
    userRepo *UserRepository

    // No guard — public endpoint
    GetProfile  func(*gin.Context) `route:"GET:/me" guard:""`

    // Single guard
    UpdateProfile func(*gin.Context) `route:"PUT:/me" guard:"auth"`

    // Multiple guards with arguments
    DeleteUser func(*gin.Context) `route:"DELETE:/users/:id" guard:"auth,role(admin)"`

    // Multiple arguments separated by commas
    ListAdmins func(*gin.Context) `route:"GET:/admins" guard:"auth,role(admin,editor)"`
}
```

### Tag Syntax

| Pattern | Meaning |
|---------|---------|
| `guard:"auth"` | Run the `auth` guard with no arguments |
| `guard:"role(admin)"` | Run the `role` guard with the argument `admin` |
| `guard:"role(admin,editor)"` | Run the `role` guard with arguments `admin` and `editor` |
| `guard:"auth,role(admin)"` | Run `auth`, then `role(admin)` in order |
| `guard:""` or omitted | No guard attached (public endpoint) |

Arguments are split on commas and trimmed of surrounding whitespace.

## Wiring It Up

Register your guards **before** loading the module that contains the controllers, otherwise `RegisterControllers` will panic when it cannot find a referenced guard:

```go
func main() {
    app := nika.NewApp()

    // 1. Register guards first
    src.RegisterGuards(app)

    // 2. Then load modules/controllers
    rootModule := src.NewAppModule()
    app.LoadModule(rootModule)

    app.Listen(":3000")
}
```

If a guard referenced in a tag has not been registered, the application will panic at startup with a clear message:

```
❌ Guard 'auth' not registered. Use app.AddGuard('auth', ...)
```

## Practical Examples

### JWT Authentication Guard

```go
app.AddGuard("jwt", func(args []string) gin.HandlerFunc {
    return func(c *gin.Context) {
        header := c.GetHeader("Authorization")
        if len(header) < 8 || header[:7] != "Bearer " {
            response.UnauthorizedRequest(c, "INVALID_TOKEN", "bearer token required")
            return
        }

        claims, err := jwtService.Parse(header[7:])
        if err != nil {
            response.UnauthorizedRequest(c, "INVALID_TOKEN", err.Error())
            return
        }

        c.Set("user", claims)
        c.Next()
    }
})
```

### Ownership Guard (with argument)

```go
app.AddGuard("owner", func(args []string) gin.HandlerFunc {
    resource := args[0] // e.g. "post", "comment"
    return func(c *gin.Context) {
        id := c.Param("id")
        user := c.MustGet("user").(UserClaims)

        if !ownsResource(resource, id, user.ID) {
            response.ForbiddenRequest(c, "FORBIDDEN", "you do not own this resource")
            return
        }
        c.Next()
    }
})

// Usage: guard:"jwt,owner(post)"
```

## Guards vs Middleware

| Aspect | Guards | Regular Middleware (`app.Use`) |
|--------|--------|-------------------------------|
| Scope | Per-route, declared via struct tag | Global, applied to all routes |
| Configuration | Arguments parsed from the tag | Manual closure over variables |
| Registration | Named, via `app.AddGuard` | Inline function |
| Ordering | Declared left-to-right in the tag | Order of `app.Use` calls |

Guards are compiled into the route's handler chain at registration time, so there is **no per-request reflection cost** — they run as fast as any other Gin middleware.

## Status

| Feature | Status |
|---------|--------|
| Named function guards | ✅ Implemented |
| Guard arguments via tag | ✅ Implemented |
| Multiple guards per route | ✅ Implemented |
| Ordered guard chains | ✅ Implemented |
| Global guards via middleware | ✅ Use `app.Use` |
| Role-based access control | ✅ Implementable with the `role` pattern |
