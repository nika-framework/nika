# Dynamic Modules

> **Coming Soon** — This feature is not yet implemented.

Dynamic modules allow you to create modules that can be customized and configured when imported by other modules. This enables configurable, reusable module packages.

## Planned Design

```go
// Planned API (subject to change)
// A dynamic module accepts configuration options
type DatabaseModuleConfig struct {
    URI      string
    Database string
}

func NewDatabaseModule(cfg DatabaseModuleConfig) *DatabaseModule {
    return &DatabaseModule{
        uri:      cfg.URI,
        database: cfg.Database,
    }
}

func (m *DatabaseModule) Providers() []interface{} {
    return []interface{}{
        func() *MongoDB {
            return connectMongoDB(m.uri, m.database)
        },
    }
}
```

## Usage (Planned)

```go
func (m *AppModule) Imports() []nika.Module {
    return []nika.Module{
        // Import with custom configuration
        NewDatabaseModule(DatabaseModuleConfig{
            URI:      "mongodb://localhost:27017",
            Database: "myapp",
        }),
    }
}
```

## Status

| Feature | Status |
|---------|--------|
| Dynamic module interface | ⏳ Planned |
| Configurable module options | ⏳ Planned |
| Register providers dynamically | ⏳ Planned |
| Module forRoot / forChild pattern | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution. Check out the [GitHub repository](https://github.com/nika-framework/nika) for guidelines.
