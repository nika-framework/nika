# Configuration

Nika provides a simple, environment-based configuration system through the `common/config` package. It uses `.env` files to manage environment variables.

## Setup

Install the config package and initialize it in your application:

```go
package main

import (
    "github.com/sajadweb/nika"
    "github.com/sajadweb/nika/common/config"
)

func main() {
    app := nika.NewApp()

    // Load .env file and register Config in DI container
    config.Setup(app, ".env")

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## `.env` File

Create a `.env` file in your project root:

```env
APP_NAME=MyNikaApp
APP_PORT=3000
APP_DEBUG=true

# Database
MONGO_URI=mongodb://localhost:27017
MONGO_DATABASE=myapp

# Cache
CACHE_DRIVER=redis
CACHE_URL=redis://localhost:6379

# JWT
JWT_SECRET=your-super-secret-key
JWT_EXPIRES_IN=3600
```

## Using Config in Providers

Once `config.Setup()` is called, the `*config.Config` instance is available in the DI container:

```go
package src

import "github.com/sajadweb/nika/common/config"

type DatabaseService struct {
    config *config.Config
}

func NewDatabaseService(cfg *config.Config) *DatabaseService {
    return &DatabaseService{config: cfg}
}

func (s *DatabaseService) Connect() {
    uri := s.config.Get("MONGO_URI", "mongodb://localhost:27017")
    dbName := s.config.Get("MONGO_DATABASE", "myapp")
    port := s.config.GetInt("APP_PORT", 3000)
    debug := s.config.GetBool("APP_DEBUG", false)

    // Use the values...
}
```

## API Reference

### `Setup(app *nika.App, envPath string) *Config`

Loads the `.env` file and registers `*Config` in the DI container.

| Parameter | Type | Description |
|-----------|------|-------------|
| `app` | `*nika.App` | The Nika application instance |
| `envPath` | `string` | Path to `.env` file. Empty string loads `.env` from current directory |

### `Get(key string, defaultValue ...string) string`

Returns the environment variable as a string.

```go
value := cfg.Get("APP_NAME")                    // Returns "" if not found
value := cfg.Get("APP_NAME", "DefaultApp")     // Returns "DefaultApp" if not found
```

### `GetInt(key string, defaultValue ...int) int`

Returns the environment variable parsed as an integer.

```go
port := cfg.GetInt("APP_PORT")              // Returns 0 if not found
port := cfg.GetInt("APP_PORT", 3000)        // Returns 3000 if not found
```

### `GetBool(key string, defaultValue ...bool) bool`

Returns the environment variable parsed as a boolean.

```go
debug := cfg.GetBool("APP_DEBUG")            // Returns false if not found
debug := cfg.GetBool("APP_DEBUG", true)      // Returns true if not found
```

## Complete Example

```go
package main

import (
    "fmt"
    "github.com/sajadweb/nika"
    "github.com/sajadweb/nika/common/config"
)

func main() {
    app := nika.NewApp()

    cfg := config.Setup(app, ".env")

    // Access config values
    port := cfg.GetInt("APP_PORT", 3000)
    appName := cfg.Get("APP_NAME", "NikaApp")
    debug := cfg.GetBool("APP_DEBUG", false)

    fmt.Printf("Starting %s on port %d (debug: %v)\n", appName, port, debug)

    app.LoadModule(rootModule)
    app.Listen(fmt.Sprintf(":%d", port))
}
```

## Multiple Environments

Use different `.env` files for different environments:

=== ".env.development"
    ```env
    APP_DEBUG=true
    MONGO_URI=mongodb://localhost:27017
    ```

=== ".env.production"
    ```env
    APP_DEBUG=false
    MONGO_URI=mongodb://prod-server:27017
    ```

```go
// Load different env files based on environment
env := os.Getenv("GO_ENV")
envPath := ".env"
if env != "" {
    envPath = ".env." + env
}
config.Setup(app, envPath)
```
