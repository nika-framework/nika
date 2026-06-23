# Serialization

Nika provides standardized JSON response helpers through the `common/response` package. These helpers ensure a consistent response format across your entire application.

## Setup

No setup is required — the `response` package provides standalone functions. Import and use directly:

```go
import "github.com/sajadweb/nika/common/response"
```

## Response Types

### `Response` — Standard Success Response

```go
type Response struct {
    Success bool        `json:"success"`
    Message string      `json:"message,omitempty"`
    Data    interface{} `json:"data,omitempty"`
}
```

### `BoolResponse` — Boolean Success Response

```go
type BoolResponse struct {
    Success bool   `json:"success"`
    Message string `json:"message,omitempty"`
}
```

### `Error` — Error Response

```go
type Error struct {
    Success bool         `json:"success"`
    Message string       `json:"message,omitempty"`
    Error   *ErrorDetail `json:"error,omitempty"`
}

type ErrorDetail struct {
    Code    int         `json:"code"`
    Message string      `json:"message"`
    Details interface{} `json:"details,omitempty"`
}
```

## Response Helpers

### Success Responses

| Helper | Status Code | Description |
|--------|-------------|-------------|
| `Ok(c, data)` | `200` | Return data |
| `Create(c, data)` | `201` | Resource created |
| `Update(c, data)` | `202` | Resource updated |
| `OkByMsg(c, message)` | `200` | Success message only |

### Error Responses

| Helper | Status Code | Description |
|--------|-------------|-------------|
| `BadRequest(c, code, details)` | `400` | Bad request |
| `UnprocessableEntity(c, code, details)` | `422` | Validation error |
| `NotFoundRequest(c, code, message, details)` | `404` | Not found |
| `JSONError(c, statusCode, message, details)` | Custom | Custom error |

## Usage Examples

### Success Response

```go
func (ctrl *UserController) List(c *gin.Context) {
    users := ctrl.service.FindAll()
    response.Ok(c, users)
}
```

**Response:**
```json
{
  "success": true,
  "data": [
    {"id": "1", "name": "Alice"},
    {"id": "2", "name": "Bob"}
  ]
}
```

### Created Response

```go
func (ctrl *UserController) Create(c *gin.Context) {
    user := ctrl.service.Create(dto)
    response.Create(c, user)
}
```

**Response (HTTP 201):**
```json
{
  "id": "1",
  "name": "Alice",
  "email": "alice@example.com"
}
```

### Success Message

```go
func (ctrl *UserController) Delete(c *gin.Context) {
    ctrl.service.Delete(id)
    response.OkByMsg(c, "User deleted successfully")
}
```

**Response:**
```json
{
  "success": true,
  "message": "User deleted successfully"
}
```

### Error Response

```go
func (ctrl *UserController) FindOne(c *gin.Context) {
    user := ctrl.service.FindOne(id)
    if user == nil {
        response.NotFoundRequest(c, "USER_NOT_FOUND", "User not found", nil)
        return
    }
    response.Ok(c, user)
}
```

**Response (HTTP 404):**
```json
{
  "success": false,
  "error": {
    "code": 404,
    "message": "User not found"
  }
}
```

### Validation Error

```go
if !validator.BindAndValidate(c, &dto) {
    return // Automatically sends UnprocessableEntity response
}
```

**Response (HTTP 422):**
```json
{
  "success": false,
  "error": {
    "code": 422,
    "message": "VALIDATION_ERROR",
    "details": [
      {"field": "Name", "message": "Must be at least 3 characters"},
      {"field": "Email", "message": "Invalid email format"}
    ]
  }
}
```

### Custom Error Response

```go
response.JSONError(c, 403, "FORBIDDEN", "You don't have permission")
```

## Custom Response Builder

Use the constructor functions to build custom response structures:

```go
// Build a custom response
resp := response.NewResponse("Users fetched successfully", users)
c.JSON(http.StatusOK, resp)

// Build a custom error
err := response.NewError(500, "DATABASE_ERROR", "Failed to connect to database")
c.JSON(http.StatusInternalServerError, err)

// Build a boolean response
resp := response.BooleanSuccess("Operation completed")
c.JSON(http.StatusOK, resp)
```
