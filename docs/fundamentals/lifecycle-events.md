# Lifecycle Events

> **Coming Soon** — This feature is not yet implemented.

Lifecycle events allow you to hook into the application bootstrap and shutdown process. They are useful for initializing resources, establishing connections, and performing cleanup.

## Planned Lifecycle Hooks

```go
// Planned API (subject to change)
type OnModuleInit interface {
    OnModuleInit()
}

type OnModuleDestroy interface {
    OnModuleDestroy()
}

type OnApplicationBootstrap interface {
    OnApplicationBootstrap()
}

type OnApplicationShutdown interface {
    OnApplicationShutdown()
}
```

## Planned Example

```go
type DatabaseService struct {
    client *mongo.Client
}

func (s *DatabaseService) OnModuleInit() {
    // Called when the module is initialized
    log.Println("DatabaseService initializing...")
}

func (s *DatabaseService) OnApplicationBootstrap() {
    // Called after all modules are loaded
    log.Println("DatabaseService ready, running migrations...")
}

func (s *DatabaseService) OnApplicationShutdown() {
    // Called when the application is shutting down
    log.Println("DatabaseService closing connections...")
    s.client.Disconnect(context.Background())
}
```

## Lifecycle Order

```
1. LoadModule(rootModule)
   ├── For each sub-module:
   │     ├── Register Providers
   │     └── Call OnModuleInit() (planned)
2. Register Controllers
3. Call OnApplicationBootstrap() (planned)
4. App.Listen() — Server starts
5. Shutdown signal received
6. Call OnApplicationShutdown() (planned)
```

## Current Alternative

Until lifecycle hooks are implemented, you can initialize resources in constructor functions:

```go
func NewDatabaseService(cfg *config.Config) *DatabaseService {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Get("MONGO_URI")))
    if err != nil {
        panic(fmt.Sprintf("Failed to connect to MongoDB: %v", err))
    }

    log.Println("✅ DatabaseService initialized")
    return &DatabaseService{client: client}
}
```

## Status

| Hook | Status |
|------|--------|
| `OnModuleInit` | ⏳ Planned |
| `OnModuleDestroy` | ⏳ Planned |
| `OnApplicationBootstrap` | ⏳ Planned |
| `OnApplicationShutdown` | ⏳ Planned |
| Graceful shutdown | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/sajadweb/nika) for guidelines.
