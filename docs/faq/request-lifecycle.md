# Request Lifecycle

Understanding how Nika processes a request helps with debugging and building middleware.

## Lifecycle Steps

```
HTTP Request
    │
    ▼
┌─────────────────────┐
│   Gin Engine         │
│   (Router)           │
└───────┬─────────────┘
        │
        ▼
┌─────────────────────┐
│   Global Middleware   │  ← app.Use()
│   (in order)         │
└───────┬─────────────┘
        │
        ▼
┌─────────────────────┐
│   Route Handler       │  ← Controller method
│   (func(*gin.Context)) │
└───────┬─────────────┘
        │
        ▼
┌─────────────────────┐
│   HTTP Response       │
└─────────────────────┘
```

## Detailed Flow

1. **HTTP Request arrives** at the Gin engine
2. **Global Middleware** is executed in registration order
3. **Gin Router** matches the request to a registered route
4. **Route Handler** (controller method) is called with `*gin.Context`
5. **Response** is written to the client

## Application Bootstrap

```
main()
  │
  ├── nika.NewApp()
  │     └── Creates gin.Default() engine + empty DI container
  │
  ├── app.Use(middleware...)
  │     └── Registers global Gin middleware
  │
  ├── config.Setup(app, "")
  │     └── Loads .env, registers *Config in DI
  │
  ├── mongodb.Setup(app, cfg)
  │     └── Connects to MongoDB, registers *MongoDB & *mongo.Database in DI
  │
  ├── cache.Setup(app, cfg)
  │     └── Creates cache provider, registers *Cache in DI
  │
  ├── validator.Setup(app)
  │     └── Registers *validator.Validate in DI
  │
  ├── app.LoadModule(rootModule)
  │     ├── LoadModule(subModule1) ──→ Register providers
  │     ├── LoadModule(subModule2) ──→ Register providers
  │     ├── Register providers ──────→ Resolve deps & register
  │     └── Register controllers ───→ Resolve deps & register routes
  │
  └── app.Listen(":3000")
        └── gin.Engine.Run() — starts HTTP server
```

## DI Resolution Order

1. Sub-modules are loaded first (recursive)
2. All providers are registered (constructors called, deps resolved)
3. Controllers are resolved (deps injected from container)
4. Routes are registered on the Gin engine
