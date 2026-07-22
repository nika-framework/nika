# Database

Nika provides first-class database support for both **NoSQL** and **SQL** databases through dedicated packages, each featuring a **generic repository pattern** with type-safe CRUD operations.

## Available Database Packages

| Database | Package | Status |
|----------|---------|--------|
| MongoDB | `common/mongodb` | ✅ Implemented |
| PostgreSQL | `common/sqldb` | ✅ Implemented |
| MySQL | `common/sqldb` | ✅ Implemented |
| SQLite | `common/sqldb` | ✅ Implemented |

## MongoDB

Nika's MongoDB integration provides connection management, a generic repository pattern, and aggregation pipeline support.

→ See the [Mongo](mongodb.md) section for complete documentation.

```go
import "github.com/nika-framework/nika/common/mongodb"

db, err := mongodb.Setup(app, mongodb.Config{
    URI:      "mongodb://localhost:27017",
    Database: "myapp",
})
```

## SQL Database (PostgreSQL / MySQL / SQLite)

Nika's SQL integration provides connection pooling, a generic repository pattern with parameterized queries, transaction helpers, upsert, batch inserts, and pagination.

→ See the [SQL Database](sql.md) section for complete documentation.

```go
import "github.com/nika-framework/nika/common/sqldb"

db, err := sqldb.Setup(app, sqldb.Config{
    Driver:       sqldb.DriverPostgres,
    DSN:          "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
    Database:     "mydb",
    MaxOpenConns: 25,
    MaxIdleConns: 5,
})
```

## Feature Comparison

| Feature | MongoDB | SQL |
|---------|---------|-----|
| Generic repository | ✅ `BaseRepository[T]` | ✅ `BaseRepository[T, ID]` |
| Connection pooling | ✅ via driver | ✅ `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime` |
| Transactions | ✅ MongoDB sessions | ✅ `WithTransaction` / `WithTransactionResult` |
| Pagination | ✅ `$facet` aggregation | ✅ `COUNT(*)` + `LIMIT/OFFSET` |
| Upsert | ✅ `CreateAndUpdate` | ✅ `INSERT ... ON CONFLICT` |
| Batch insert | ✅ `InsertMany` | ✅ `InsertMany` (multi-row) |
| Raw queries | ✅ `FindWithAggregate` | ✅ `RawQuery` / `RawExec` |
| DI integration | ✅ | ✅ |
| Increment/Decrement | ✅ | ✅ |

## Status

| Feature | Status |
|---------|--------|
| MongoDB connection & repository | ✅ Implemented |
| SQL connection & repository | ✅ Implemented |
| Connection pooling | ✅ Implemented |
| Transaction helpers | ✅ Implemented |
| Database migrations | ⏳ Planned |
