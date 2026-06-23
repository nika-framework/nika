# Common Errors

## DI Resolution Errors

### ❌ Cannot resolve type for constructor

```
❌ DI Error: Cannot resolve '*UserService' for constructor
```

**Cause:** The required dependency is not registered in the DI container.

**Solution:** Make sure the dependency is registered as a provider in some module:

```go
// Check that the dependency is in a module's Providers()
func (m *UsersModule) Providers() []interface{} {
    return []interface{}{
        NewUserService,     // ← Must be registered
        NewUserRepository,  // ← Must be registered
    }
}
```

### ❌ Module import order issue

**Cause:** Provider A depends on Provider B, but B's module is imported after A's module.

**Solution:** Import the module containing Provider B first:

```go
func (m *AppModule) Imports() []nika.Module {
    return []nika.Module{
        NewDatabaseModule(),  // ← Import first (provides Database)
        NewUsersModule(),     // ← UsersModule depends on Database
    }
}
```

## Controller Errors

### ❌ Controller must be a pointer to a struct

```
panic: Controller must be a pointer to a struct
```

**Cause:** You passed a non-pointer to the controller.

**Solution:**

```go
// Wrong
func (m *AppModule) Controllers() []interface{} {
    return []interface{}{UserController{}} // ← Not a pointer
}

// Correct
func (m *AppModule) Controllers() []interface{} {
    return []interface{}{NewUserController} // ← Pass constructor
}
```

### ❌ Invalid route tag

```
panic: Invalid route tag in List
```

**Cause:** The `route` tag is malformed.

**Solution:** Use the correct format `METHOD:path`:

```go
// Wrong
List func(*gin.Context) `route:"GET/users"`      // Missing colon
List func(*gin.Context) `route:"GET:/users" `    // Extra space

// Correct
List func(*gin.Context) `route:"GET:/users"`
```

### ❌ Field must be a function

```
panic: Field List must be a function
```

**Cause:** The tagged field is not a function.

**Solution:** Ensure the field type is `func(*gin.Context)`.

### ❌ Route handler field must be exported

```
panic: Route handler field list must be exported
```

**Cause:** The field name starts with a lowercase letter.

**Solution:** Use uppercase for route handler fields:

```go
// Wrong
list func(*gin.Context) `route:"GET:/users"`   // lowercase

// Correct
List func(*gin.Context) `route:"GET:/users"`    // Uppercase
```

## Unsupported Method

```
panic: Unsupported method: HEAD
```

**Cause:** The HTTP method is not supported.

**Supported methods:** `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`

## Validator Errors

### ❌ validator: not initialized

```
panic: validator: not initialized — call validator.Setup(app, ...) before use
```

**Cause:** `validator.Setup()` was not called before using validation helpers.

**Solution:** Call Setup in your main function:

```go
func main() {
    app := nika.NewApp()
    validator.Setup(app) // ← Add this
    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Cache Errors

### ❌ unknown cache driver

```
unknown cache driver: postgres
```

**Supported drivers:** `redis`, `file`

### ❌ memcached provider not implemented

The memcached driver is not yet implemented. Use `redis` or `file` instead.
