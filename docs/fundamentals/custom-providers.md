# Custom Providers

Nika's DI container provides several ways to register and customize providers beyond simple constructor functions.

## Standard Provider (Constructor Function)

The most common way to register a provider is through a constructor function. Nika resolves all dependencies automatically:

```go
func NewUserService(repo *UserRepository, cache *cache.Cache) *UserService {
    return &UserService{
        repo:  repo,
        cache: cache,
    }
}

func (m *UsersModule) Providers() []interface{} {
    return []interface{}{
        NewUserService, // Nika resolves *UserRepository and *cache.Cache
    }
}
```

## Instance Provider

You can provide a pre-constructed instance directly:

```go
func (m *ConfigModule) Providers() []interface{} {
    return []interface{}{
        &ConfigService{
            DBHost:     "localhost",
            DBPort:     5432,
            CacheTTL:   300,
        },
    }
}
```

## RegisterSingleton

Use `app.RegisterSingleton()` to register a singleton directly in the `App` container, before modules are loaded:

```go
func main() {
    app := nika.NewApp()

    // Register singletons before loading modules
    db, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{})
    app.RegisterSingleton(db)

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Interface-based Providers

When a constructor returns an interface, Nika registers both the concrete type and the interface:

```go
type CacheProvider interface {
    Get(ctx context.Context, key string) (string, error)
    Set(ctx context.Context, key string, value any, ttl time.Duration) error
}

func NewRedisCacheProvider(url string) CacheProvider {
    return &redisProvider{client: redis.NewClient(...)}
}

// Nika registers the instance under:
// - *redisProvider (concrete type)
// - CacheProvider (interface return type)
```

## Provider with Setup Pattern

Common packages use the `Setup()` pattern, which internally calls `RegisterSingleton`:

```go
func Setup(app *nika.App, cfg Config) (*Cache, error) {
    provider, err := NewRedisProvider(cfg.URL)
    if err != nil {
        return nil, err
    }
    cache := &Cache{Provider: provider}
    app.RegisterSingleton(cache)
    return cache, nil
}
```

## Dependency Resolution Chain

Nika resolves dependencies in the order modules are loaded:

```
LoadModule(AppModule)
  ├── LoadModule(ConfigModule)
  │     └── Register: *Config
  ├── LoadModule(DatabaseModule)
  │     └── Register: *MongoDB, *mongo.Database
  ├── LoadModule(UsersModule)
  │     ├── Register: *UserRepository (needs *mongo.Database ✓)
  │     └── Register: *UserService (needs *UserRepository ✓)
  └── Register Controllers
        └── UserController (needs *UserService ✓)
```

## Summary

| Method | Auto-resolve | When to Use |
|--------|-------------|-------------|
| Constructor function | ✅ | Most providers with dependencies |
| Direct instance | ❌ | Simple config or constant values |
| `RegisterSingleton` | ❌ | External resources, pre-built instances |
| `Setup()` pattern | ✅ | Common package integration |
