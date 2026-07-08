# Database

> **Coming Soon** — A unified database package is not yet implemented.

Currently, Nika provides MongoDB support through the `common/mongodb` package. SQL database support (PostgreSQL, MySQL, SQLite) is planned for future releases.

## Available Database Packages

| Database | Package | Status |
|----------|---------|--------|
| MongoDB | `common/mongodb` | ✅ Implemented |
| PostgreSQL | — | ⏳ Planned |
| MySQL | — | ⏳ Planned |
| SQLite | — | ⏳ Planned |

## MongoDB Integration

See the [Mongo](mongodb.md) section for complete MongoDB documentation.

## SQL Database (Planned)

```go
// Planned API (subject to change)
import "github.com/nika-framework/nika/common/sql"

func Setup(app *nika.App, cfg sql.Config) (*sql.Database, error) {
    // Connect to SQL database
    // Register in DI container
}
```

## Status

| Feature | Status |
|---------|--------|
| MongoDB connection | ✅ Implemented |
| Generic repository pattern | ✅ Implemented |
| SQL connection | ⏳ Planned |
| SQL ORM integration | ⏳ Planned |
| Database migrations | ⏳ Planned |
| Connection pooling | ⏳ Planned |
