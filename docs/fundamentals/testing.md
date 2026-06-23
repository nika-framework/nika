# Testing

> **Coming Soon** — Testing utilities are not yet implemented.

This chapter covers how to test Nika applications, including unit tests for providers and integration tests for controllers.

## Current State

Nika does not yet provide dedicated testing utilities. However, you can test your application using standard Go testing tools.

## Testing Providers (Unit Tests)

Since providers are plain Go structs, you can test them directly:

```go
package src_test

import (
    "testing"
    "myapp/src"
)

// Test with a mock repository
type MockUserRepository struct {
    users []User
}

func (m *MockUserRepository) FindAll() []User {
    return m.users
}

func (m *MockUserRepository) Create(user *User) *User {
    m.users = append(m.users, *user)
    return user
}

func TestUserService_FindAll(t *testing.T) {
    // Arrange
    mockRepo := &MockUserRepository{
        users: []User{
            {Name: "Alice"},
            {Name: "Bob"},
        },
    }
    service := src.NewUserService(mockRepo)

    // Act
    users := service.FindAll()

    // Assert
    if len(users) != 2 {
        t.Errorf("Expected 2 users, got %d", len(users))
    }
}
```

## Testing HTTP Handlers

You can test Gin handlers using `net/http/httptest`:

```go
package src_test

import (
    "net/http"
    "net/http/httptest"
    "testing"
    "encoding/json"
    "bytes"
    "github.com/gin-gonic/gin"
)

func TestCreateUser(t *testing.T) {
    gin.SetMode(gin.TestMode)
    router := gin.New()

    // Setup your controller
    service := src.NewUserService(mockRepo)
    ctrl := src.NewUserController(service)

    router.POST("/users", ctrl.Create)

    // Create request
    body, _ := json.Marshal(map[string]string{
        "name":  "Alice",
        "email": "alice@example.com",
    })
    req, _ := httptest.NewRequest("POST", "/users", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    router.ServeHTTP(w, req)

    // Assert
    if w.Code != http.StatusCreated {
        t.Errorf("Expected status 201, got %d", w.Code)
    }
}
```

## Planned Testing Utilities

```go
// Planned: Test module and application builder
func TestApp(t *testing.T) {
    app := nika.NewTestApp()

    app.LoadModule(NewTestModule())

    // Make HTTP requests
    resp := app.Get("/users")
    assert.Equal(t, 200, resp.StatusCode)
    assert.JSONEq(t, `{"users": [...]}`, resp.Body)

    resp = app.Post("/users", `{"name": "Alice"}`)
    assert.Equal(t, 201, resp.StatusCode)
}
```

## Status

| Feature | Status |
|---------|--------|
| Unit testing providers | ✅ Standard Go testing |
| HTTP handler testing | ✅ httptest + Gin test mode |
| Test application builder | ⏳ Planned |
| Mock DI container | ⏳ Planned |
| E2E testing utilities | ⏳ Planned |

!!! info "Want to contribute?"
    Testing utilities are open for contribution. Check out the [GitHub repository](https://github.com/sajadweb/nika) for guidelines.
