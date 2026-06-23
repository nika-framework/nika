# Cookies

> **Coming Soon** — A dedicated cookie package is not yet implemented.

Since Nika is built on Gin, you can use Gin's built-in cookie handling.

## Current Approach

```go
func (ctrl *UserController) SetCookie(c *gin.Context) {
    // Set a cookie
    c.SetCookie("token", "abc123", 3600, "/", "localhost", false, true)
}

func (ctrl *UserController) GetCookie(c *gin.Context) {
    // Read a cookie
    token, err := c.Cookie("token")
    if err != nil {
        c.JSON(401, gin.H{"error": "Cookie not found"})
        return
    }
    c.JSON(200, gin.H{"token": token})
}

func (ctrl *UserController) ClearCookie(c *gin.Context) {
    // Delete a cookie
    c.SetCookie("token", "", -1, "/", "localhost", false, true)
}
```

## Status

| Feature | Status |
|---------|--------|
| Set/Get/Delete cookies | ✅ Available via Gin |
| Signed cookies | ✅ Available via Gin |
| Cookie parser package | ⏳ Planned |
