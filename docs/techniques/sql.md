# SQL Database

Nika provides a complete SQL database integration through the `common/sqldb` package, supporting **PostgreSQL**, **MySQL**, and **SQLite** with a **generic repository pattern** for type-safe CRUD operations, transactions, pagination, and more.

## Installation

```bash
go get github.com/nika-framework/nika
```

You also need to install the appropriate database driver for your chosen database:

=== "PostgreSQL"

    ```bash
    go get github.com/lib/pq
    ```

=== "MySQL"

    ```bash
    go get github.com/go-sql-driver/mysql
    ```

=== "SQLite"

    ```bash
    go get github.com/mattn/go-sqlite3
    ```

## Setup

### Connection

```go
package main

import (
    "github.com/nika-framework/nika"
    "github.com/nika-framework/nika/common/sqldb"

    _ "github.com/lib/pq" // PostgreSQL driver
)

func main() {
    app := nika.NewApp()

    _, err := sqldb.Setup(app, sqldb.Config{
        Driver:       sqldb.DriverPostgres,
        DSN:          "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
        Database:     "mydb",
        MaxOpenConns: 25,
        MaxIdleConns: 5,
    })
    if err != nil {
        panic(err)
    }

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Configuration with `.env`

```go
cfg := config.Setup(app, "")

db, err := sqldb.Setup(app, sqldb.Config{
    Driver:       sqldb.Driver(cfg.Get("DB_DRIVER", "postgres")),
    DSN:          cfg.Get("DB_DSN", "postgres://localhost:5432/mydb?sslmode=disable"),
    Database:     cfg.Get("DB_NAME", "mydb"),
    MaxOpenConns: 25,
    MaxIdleConns: 5,
})
```

### Supported Drivers

| Driver | Constant | DSN Example |
|--------|----------|-------------|
| PostgreSQL | `sqldb.DriverPostgres` | `postgres://user:pass@localhost:5432/dbname?sslmode=disable` |
| MySQL | `sqldb.DriverMySQL` | `user:pass@tcp(localhost:3306)/dbname?parseTime=true` |
| SQLite | `sqldb.DriverSQLite` | `file:test.db?cache=shared&mode=rwc` |

### Connection Pool Configuration

| Option | Description | Default |
|--------|-------------|---------|
| `MaxOpenConns` | Maximum number of open connections | Unlimited |
| `MaxIdleConns` | Maximum number of idle connections in the pool | 2 |
| `ConnMaxLifetime` | Maximum time a connection may be reused | Unlimited |
| `ConnMaxIdleTime` | Maximum time a connection may be idle | Unlimited |

```go
lifetime := 30 * time.Minute
idleTime := 5 * time.Minute

db, err := sqldb.Setup(app, sqldb.Config{
    Driver:          sqldb.DriverPostgres,
    DSN:             "postgres://localhost:5432/mydb?sslmode=disable",
    Database:        "mydb",
    MaxOpenConns:    25,
    MaxIdleConns:    5,
    ConnMaxLifetime: &lifetime,
    ConnMaxIdleTime: &idleTime,
})
```

### What Gets Registered

`sqldb.Setup()` registers two types in the DI container:

| Registered Type | How to Inject |
|-----------------|---------------|
| `*sqldb.DB` | The Nika SQL database wrapper |
| `*sql.DB` | The native `database/sql` connection |

```go
// Inject the Nika wrapper
func NewUserService(db *sqldb.DB) *UserService { ... }

// Or inject the native sql.DB directly
func NewUserService(db *sql.DB) *UserService { ... }
```

## Generic Repository Pattern

The `common/sqldb/repository` package provides a **generic base repository** with type-safe CRUD operations, similar to the MongoDB repository but designed specifically for SQL databases.

### Define Your Model

Use the `db` struct tag to map fields to database columns:

```go
package models

import "time"

type User struct {
    ID        int64     `json:"id" db:"id"`
    Name      string    `json:"name" db:"name"`
    Email     string    `json:"email" db:"email"`
    Age       int       `json:"age" db:"age"`
    Role      string    `json:"role" db:"role"`
    IsActive  bool      `json:"is_active" db:"is_active"`
    CreatedAt time.Time `json:"created_at" db:"created_at"`
    UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}
```

!!! tip "Struct Tag Convention"
    Use `db:"column_name"` to map struct fields to SQL columns. Fields without a `db` tag or with `db:"-"` are ignored by the repository.

### Create a Repository

```go
package src

import (
    "github.com/nika-framework/nika/common/sqldb"
    "github.com/nika-framework/nika/common/sqldb/repository"
)

type UserRepository struct {
    repo *repository.BaseRepository[models.User, int64]
}

func NewUserRepository(db *sqldb.DB) *UserRepository {
    return &UserRepository{
        repo: repository.NewBaseRepository[models.User, int64](
            db.Conn,      // *sql.DB connection
            "users",      // table name
            "id",         // primary key column
            true,         // auto-increment ID (excluded from INSERT)
        ),
    }
}
```

The `NewBaseRepository` constructor accepts two type parameters:

| Parameter | Description | Example |
|-----------|-------------|---------|
| `T` | The model struct type | `models.User` |
| `ID` | The primary key type | `int64`, `string`, `uuid.UUID` |

And the following arguments:

| Argument | Description |
|----------|-------------|
| `db` | The `*sql.DB` connection |
| `tableName` | The SQL table name |
| `idColumn` | The primary key column name |
| `autoIncrementID` | If `true`, excludes the ID column from INSERT statements |

!!! info "Reflection Cache"
    Struct metadata (column names, field indices) is computed **once** during construction via reflection and cached for the lifetime of the repository. This means zero reflection overhead on individual queries.

### Available Methods

#### Create Operations

```go
// Create a single record (uses RETURNING to get the generated ID)
user := &models.User{Name: "Alice", Email: "alice@example.com", Role: "admin"}
created, err := userRepo.repo.Create(ctx, user)
fmt.Println(created.ID) // → auto-generated ID

// Create within a transaction
tx, _ := db.Conn.BeginTx(ctx, nil)
created, err := userRepo.repo.CreateTx(ctx, tx, user)
tx.Commit()

// Insert many (batch INSERT in a single query)
users := []models.User{
    {Name: "Alice", Email: "alice@test.com"},
    {Name: "Bob", Email: "bob@test.com"},
    {Name: "Charlie", Email: "charlie@test.com"},
}
affected, err := userRepo.repo.InsertMany(ctx, users)
// affected → 3
```

#### Read Operations

```go
// Find one by ID
user, err := userRepo.repo.FindOneByID(ctx, 42)

// Find one by filter
user, err := userRepo.repo.FindOne(ctx, repository.Filter{"email": "alice@example.com"})

// Find by condition
admins, err := userRepo.repo.FindByCondition(ctx, repository.Filter{"role": "admin"})

// Find all (with optional filter, pass nil or empty map for all)
allUsers, err := userRepo.repo.FindAll(ctx, nil)

// Check existence
exists, err := userRepo.repo.ExistsByID(ctx, 42)
exists, err := userRepo.repo.ExistsByCondition(ctx, repository.Filter{"email": "alice@example.com"})

// Count
count, err := userRepo.repo.CountByCondition(ctx, repository.Filter{"role": "admin"})

// Raw SQL query (returns typed results)
users, err := userRepo.repo.RawQuery(ctx,
    "SELECT id, name, email, age, role, is_active, created_at, updated_at FROM users WHERE age > $1 ORDER BY name",
    25,
)
```

!!! note "Nil vs Empty Filter"
    When `FindOne` or `FindByCondition` receive a `nil` or empty filter, the query runs without a `WHERE` clause (returns all records).

#### Update Operations

```go
// Update by ID
err := userRepo.repo.UpdateOneByID(ctx, 42, repository.Filter{"name": "Alice Updated"})

// Update one by condition
err := userRepo.repo.UpdateOne(ctx,
    repository.Filter{"email": "old@example.com"},
    repository.Filter{"email": "new@example.com"},
)

// Update and return the updated record (uses RETURNING)
updated, err := userRepo.repo.UpdateAndFindOne(ctx,
    repository.Filter{"email": "alice@example.com"},
    repository.Filter{"name": "Alice Smith"},
)

// Update many (returns affected row count)
affected, err := userRepo.repo.UpdateMany(ctx,
    repository.Filter{"role": "user"},
    repository.Filter{"is_active": true},
)

// Increment a numeric column
err := userRepo.repo.Increment(ctx, repository.Filter{"id": 42}, "login_count", 1)

// Decrement a numeric column
err := userRepo.repo.Decrement(ctx, repository.Filter{"id": 42}, "credits", 10)

// Raw SQL execution
result, err := userRepo.repo.RawExec(ctx,
    "UPDATE users SET last_login = NOW() WHERE id = $1", 42,
)
```

#### Delete Operations

```go
// Delete by ID
err := userRepo.repo.DeleteByID(ctx, 42)

// Delete one by condition
err := userRepo.repo.DeleteOne(ctx, repository.Filter{"email": "old@example.com"})

// Delete many (returns deleted row count)
deleted, err := userRepo.repo.DeleteMany(ctx, repository.Filter{"is_active": false})
```

#### Upsert (INSERT ... ON CONFLICT)

```go
// Upsert: insert or update on conflict (PostgreSQL)
user := &models.User{Name: "Alice", Email: "alice@example.com", Role: "admin"}
result, err := userRepo.repo.Upsert(ctx, user, "email") // conflict on "email" column
```

This generates:

```sql
INSERT INTO users (name, email, role, ...) VALUES ($1, $2, $3, ...)
ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name, role = EXCLUDED.role, ...
RETURNING id, name, email, ...
```

## Pagination

The `Pages` method performs efficient server-side pagination with a total count:

```go
func (ctrl *UserController) List(c *gin.Context) {
    page, _ := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 64)
    perPage, _ := strconv.ParseInt(c.DefaultQuery("per_page", "10"), 10, 64)

    result, err := ctrl.userRepo.repo.Pages(ctx,
        repository.Filter{"is_active": true}, // filter
        page,                                   // page number (1-based)
        perPage,                                // items per page
        repository.OrderBy{Column: "created_at", Desc: true}, // sorting
    )
    if err != nil {
        response.JSONError(c, 500, "DB_ERROR", err.Error())
        return
    }

    response.Ok(c, gin.H{
        "data":        result.Data,
        "total":       result.Total,
        "page":        result.Page,
        "per_page":    result.PerPage,
        "total_pages": result.TotalPages,
    })
}
```

The `PaginationResult` struct:

```go
type PaginationResult[T any] struct {
    Data       []T   `json:"data"`
    Total      int64 `json:"total"`
    Page       int64 `json:"page"`
    PerPage    int64 `json:"perPage"`
    TotalPages int64 `json:"totalPages"`
}
```

## Transactions

Nika provides two transaction helper functions that handle `Begin`, `Commit`, `Rollback`, and **panic recovery** automatically:

### Basic Transaction

```go
import "github.com/nika-framework/nika/common/sqldb/repository"

err := repository.WithTransaction(ctx, db.Conn, func(tx *sql.Tx) error {
    // All operations here run in a single transaction
    _, err := userRepo.repo.CreateTx(ctx, tx, &models.User{
        Name:  "Alice",
        Email: "alice@example.com",
    })
    if err != nil {
        return err // triggers automatic ROLLBACK
    }

    // Do more work within the same transaction...
    _, err = tx.ExecContext(ctx,
        "INSERT INTO audit_log (action, user_email) VALUES ($1, $2)",
        "user_created", "alice@example.com",
    )
    return err
})
// If fn returns nil → COMMIT
// If fn returns error → ROLLBACK
// If fn panics → ROLLBACK + re-panic
```

### Transaction with Return Value

```go
user, err := repository.WithTransactionResult[*models.User](ctx, db.Conn, func(tx *sql.Tx) (*models.User, error) {
    created, err := userRepo.repo.CreateTx(ctx, tx, &models.User{
        Name:  "Alice",
        Email: "alice@example.com",
    })
    if err != nil {
        return nil, err
    }
    return created, nil
})
```

## Helper Functions

```go
import "github.com/nika-framework/nika/common/sqldb/repository"

// Null-safe value constructors
ns := repository.NullString("hello")    // sql.NullString{String: "hello", Valid: true}
ni := repository.NullInt64(42)          // sql.NullInt64{Int64: 42, Valid: true}
nf := repository.NullFloat64(3.14)      // sql.NullFloat64{Float64: 3.14, Valid: true}
nb := repository.NullBool(true)         // sql.NullBool{Bool: true, Valid: true}
nt := repository.NullTime(time.Now())   // sql.NullTime{Time: now, Valid: true}

// Empty/zero values → NULL
ns := repository.NullString("")         // sql.NullString{Valid: false}
ni := repository.NullInt64(0)           // sql.NullInt64{Valid: false}

// LIKE pattern helpers
repository.ToLikePattern("alice")       // → "%alice%"
repository.ToStartsWith("alice")        // → "alice%"
repository.ToEndsWith("alice")          // → "%alice"

// Safe IN clause generation
clause, args := repository.InClause("status", 1, []string{"active", "pending"})
// clause → "status IN ($1, $2)"
// args   → ["active", "pending"]
```

## DB Wrapper Methods

The `sqldb.DB` wrapper provides convenience methods:

```go
// Start a transaction with options
tx, err := db.BeginTx(ctx, &sql.TxOptions{
    Isolation: sql.LevelSerializable,
    ReadOnly:  true,
})

// Health check (ping)
err := db.HealthCheck(ctx)

// Connection pool statistics
stats := db.Stats()
fmt.Printf("Open connections: %d\n", stats.OpenConnections)
fmt.Printf("In use: %d\n", stats.InUse)
fmt.Printf("Idle: %d\n", stats.Idle)

// Close the connection
defer db.Close()
```

## Performance Notes

- **Reflection cached at construction**: Struct field metadata is computed once via `NewBaseRepository` and reused for every query — no per-query reflection overhead.
- **Batch inserts**: `InsertMany` builds a single multi-row `INSERT` statement instead of individual queries, significantly reducing round-trips.
- **Parameterized queries**: All queries use `$1, $2, ...` placeholders to prevent SQL injection and enable query plan caching.
- **Pagination with COUNT**: `Pages` runs a `COUNT(*)` query alongside the data query for accurate total counts.
- **Connection pooling**: Proper pool configuration (`MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`) prevents connection leaks and optimizes throughput.

## Comparison: MongoDB vs SQL Repository

| Feature | MongoDB | SQL |
|---------|---------|-----|
| Type parameter | `BaseRepository[T]` | `BaseRepository[T, ID]` |
| Connection | `*mongo.Collection` | `*sql.DB` + table name |
| ID type | `primitive.ObjectID` | Any `comparable` (int64, string, uuid) |
| Filter type | `bson.M` | `map[string]any` |
| Create returns | `*T` with injected `_id` | `*T` via `RETURNING` |
| Pagination | `$facet` aggregation | `COUNT(*)` + `LIMIT/OFFSET` |
| Transactions | MongoDB sessions | `sql.Tx` with helpers |
| Upsert | `CreateAndUpdate` | `INSERT ... ON CONFLICT` |
| Raw queries | `FindWithAggregate` | `RawQuery` / `RawExec` |
