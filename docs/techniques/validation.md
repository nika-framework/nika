# Validation

Nika provides a powerful validation system through the `common/validator` package, built on top of [go-playground/validator](https://github.com/go-playground/validator).

## Setup

```go
package main

import (
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/validator"
)

func main() {
    app := nika.NewApp()

    // Initialize validator and register in DI container
    validator.Setup(app)

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Defining DTOs

Define your Data Transfer Objects with validation tags:

```go
package dto

type CreateUserDTO struct {
    Name     string `json:"name" validate:"required,min=3,max=50"`
    Email    string `json:"email" validate:"required,email"`
    Mobile   string `json:"mobile" validate:"required,ir_mobile"`
    Password string `json:"password" validate:"required,min=8,max=32"`
    Age      int    `json:"age" validate:"required,gte=18"`
    Role     string `json:"role" validate:"required,oneof=admin user guest"`
}

type UpdateUserDTO struct {
    Name  string `json:"name" validate:"omitempty,min=3,max=50"`
    Email string `json:"email" validate:"omitempty,email"`
}

type GetUserDTO struct {
    ID string `uri:"id" validate:"required,objectid"`
}

type ListUsersDTO struct {
    Page    int    `form:"page" validate:"omitempty,gte=1"`
    PerPage int    `form:"per_page" validate:"omitempty,gte=1,lte=100"`
    Search  string `form:"search" validate:"omitempty,min=1,max=50"`
}
```

## Validation Helpers

### `BindAndValidate(c *gin.Context, dto interface{}) bool`

Binds the JSON request body and validates the struct. Automatically sends error response on failure:

```go
func (ctrl *UserController) Create(c *gin.Context) {
    var dto CreateUserDTO

    // Binds JSON body + validates
    if !validator.BindAndValidate(c, &dto) {
        return // Error response is already sent
    }

    // dto is validated — proceed with business logic
    user := ctrl.service.Create(dto)
    response.Create(c, user)
}
```

### `BindAndValidateQuery(c *gin.Context, dto interface{}) bool`

Binds query parameters and validates:

```go
func (ctrl *UserController) List(c *gin.Context) {
    var dto ListUsersDTO

    // Binds query params + validates
    if !validator.BindAndValidateQuery(c, &dto) {
        return
    }

    users := ctrl.service.FindAll(dto.Page, dto.PerPage, dto.Search)
    response.Ok(c, users)
}
```

### `BindAndValidateUri(c *gin.Context, dto interface{}) bool`

Binds URI path parameters and validates:

```go
func (ctrl *UserController) FindOne(c *gin.Context) {
    var dto GetUserDTO

    // Binds URI params + validates (e.g., validates ObjectId format)
    if !validator.BindAndValidateUri(c, &dto) {
        return
    }

    user := ctrl.service.FindByID(dto.ID)
    response.Ok(c, user)
}
```

### `ValidateStruct(s interface{}) []FieldError`

Validates a struct without binding from request:

```go
func ValidateUser(user *User) []validator.FieldError {
    return validator.ValidateStruct(user)
}
```

## Validation Tags

### Standard Tags

| Tag | Description | Example |
|-----|-------------|---------|
| `required` | Field must not be empty | `validate:"required"` |
| `email` | Must be valid email | `validate:"email"` |
| `min` | Minimum length or value | `validate:"min=3"` |
| `max` | Maximum length or value | `validate:"max=50"` |
| `len` | Exact length | `validate:"len=10"` |
| `gt` | Greater than | `validate:"gt=0"` |
| `gte` | Greater than or equal | `validate:"gte=18"` |
| `lt` | Less than | `validate:"lt=100"` |
| `lte` | Less than or equal | `validate:"lte=100"` |
| `oneof` | Must be one of values | `validate:"oneof=red green blue"` |
| `number` | Must be a number | `validate:"number"` |
| `url` | Must be valid URL | `validate:"url"` |
| `omitempty` | Skip validation if empty | `validate:"omitempty,min=3"` |

### Custom Tags (Iran)

| Tag | Description | Pattern |
|-----|-------------|---------|
| `ir_mobile` | Iranian mobile number | `^09\d{9}$` |
| `objectid` | MongoDB ObjectId | `^[a-f0-9]{24}$` |

## Custom Validation Rules

Register additional validation rules using `validator.Set()`:

```go
// Register custom validation
err := validator.Set("phone", func(fl validator.FieldLevel) bool {
    return regexp.MustCompile(`^\+98\d{10}$`).MatchString(fl.Field().String())
})

// Use in DTO
type ContactDTO struct {
    Phone string `json:"phone" validate:"required,phone"`
}
```

## Error Response Format

When validation fails, the helper automatically responds with:

```json
{
  "success": false,
  "error": {
    "code": 422,
    "message": "VALIDATION_ERROR",
    "details": [
      {
        "field": "Name",
        "message": "Must be at least 3 characters"
      },
      {
        "field": "Email",
        "message": "Invalid email format"
      },
      {
        "field": "Mobile",
        "message": "Mobile number is not valid"
      }
    ]
  }
}
```

## Injecting Validator Directly

The `*validator.Validate` instance is registered in the DI container:

```go
import "github.com/go-playground/validator/v10"

type MyService struct {
    v *validator.Validate
}

func NewMyService(v *validator.Validate) *MyService {
    return &MyService{v: v}
}
```
