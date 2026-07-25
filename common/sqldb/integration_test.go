//go:build sqldb_integration

// Package sqldb integration tests. These need a real database and a registered
// driver, neither of which this module depends on, so they sit behind a build
// tag: `go test ./...` stays green with no database, and someone who wants them
// opts in explicitly.
//
//	go test -tags sqldb_integration ./common/sqldb/... \
//	    -sqldb.driver postgres \
//	    -sqldb.dsn 'postgres://user:pass@localhost:5432/nika_test?sslmode=disable'
//
// The driver package must also be linked in — add a blank import to a file in
// this directory guarded by the same tag, e.g. `_ "github.com/lib/pq"`. Nothing
// is added to go.mod on your behalf.
package sqldb_test

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nika-framework/nika/common/sqldb/repository"
)

var (
	driverFlag = flag.String("sqldb.driver", os.Getenv("NIKA_SQLDB_DRIVER"), "driver name: postgres, mysql, or sqlite3")
	dsnFlag    = flag.String("sqldb.dsn", os.Getenv("NIKA_SQLDB_DSN"), "data source name")
)

type user struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Email string `db:"email"`
	Age   int    `db:"age"`
}

func dialectFor(driver string) repository.Dialect {
	switch driver {
	case "mysql":
		return repository.DialectMySQL
	case "sqlite3", "sqlite":
		return repository.DialectSQLite
	default:
		return repository.DialectPostgres
	}
}

// openTestDB skips rather than fails when no target is configured, so the tag
// alone does not turn into a red build on a machine with no database.
func openTestDB(t *testing.T) (*sql.DB, repository.Dialect) {
	t.Helper()
	if *driverFlag == "" || *dsnFlag == "" {
		t.Skip("set -sqldb.driver and -sqldb.dsn (or NIKA_SQLDB_DRIVER/NIKA_SQLDB_DSN) to run integration tests")
	}
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db, err := sql.Open(*driverFlag, *dsnFlag)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dialectFor(*driverFlag)
}

func createUsersTable(t *testing.T, db *sql.DB, d repository.Dialect) {
	t.Helper()
	ctx := context.Background()

	var ddl string
	switch d {
	case repository.DialectPostgres:
		ddl = `CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			age INTEGER NOT NULL DEFAULT 0)`
	case repository.DialectMySQL:
		ddl = `CREATE TABLE users (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			email VARCHAR(255) NOT NULL UNIQUE,
			age INT NOT NULL DEFAULT 0)`
	default:
		ddl = `CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			age INTEGER NOT NULL DEFAULT 0)`
	}

	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS users"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS users")
	})
}

func TestRepositoryRoundTrip(t *testing.T) {
	db, d := openTestDB(t)
	createUsersTable(t, db, d)

	repo := repository.NewBaseRepositoryWithDialect[user, int64](db, d, "users", "id", true)
	ctx := context.Background()

	created, err := repo.Create(ctx, &user{Name: "Ada", Email: "ada@example.com", Age: 36})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("create did not populate the generated ID")
	}

	found, err := repo.FindOneByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if found == nil || found.Email != "ada@example.com" {
		t.Fatalf("find by id = %+v", found)
	}

	// A miss is (nil, nil), not an error.
	missing, err := repo.FindOne(ctx, repository.Filter{"email": "nobody@example.com"})
	if err != nil {
		t.Fatalf("find one: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for a miss, got %+v", missing)
	}

	if err := repo.Increment(ctx, repository.Filter{"id": created.ID}, "age", 4); err != nil {
		t.Fatalf("increment: %v", err)
	}
	found, _ = repo.FindOneByID(ctx, created.ID)
	if found.Age != 40 {
		t.Fatalf("age after increment = %d, want 40", found.Age)
	}

	// Placeholder numbering across SET + WHERE, with a nil predicate value.
	affected, err := repo.UpdateMany(ctx,
		repository.Filter{"email": "ada@example.com"},
		repository.Filter{"name": "Ada L.", "age": 41},
	)
	if err != nil {
		t.Fatalf("update many: %v", err)
	}
	if affected != 1 {
		t.Fatalf("update many affected %d rows, want 1", affected)
	}

	if _, err := repo.UpdateMany(ctx, repository.Filter{}, repository.Filter{"age": 0}); !errors.Is(err, repository.ErrEmptyFilter) {
		t.Fatalf("unfiltered UpdateMany = %v, want ErrEmptyFilter", err)
	}
	if _, err := repo.DeleteMany(ctx, repository.Filter{}); !errors.Is(err, repository.ErrEmptyFilter) {
		t.Fatalf("unfiltered DeleteMany = %v, want ErrEmptyFilter", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := repo.Create(ctx, &user{
			Name:  fmt.Sprintf("user%d", i),
			Email: fmt.Sprintf("user%d@example.com", i),
			Age:   20 + i,
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	page, err := repo.Pages(ctx, nil, 1, 2, repository.OrderBy{Column: "age", Desc: true})
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	if page.Total != 6 || len(page.Data) != 2 || page.TotalPages != 3 {
		t.Fatalf("pages = %+v", page)
	}

	keyset, err := repo.KeysetPage(ctx, nil, nil, 2, false)
	if err != nil {
		t.Fatalf("keyset: %v", err)
	}
	if len(keyset.Data) != 2 || !keyset.HasMore || keyset.NextCursor == nil {
		t.Fatalf("keyset = %+v", keyset)
	}

	byWhere, err := repo.FindByWhere(ctx, []repository.Cond{
		repository.Between("age", 20, 22),
		repository.In("name", []string{"user0", "user1", "user2"}),
	})
	if err != nil {
		t.Fatalf("find by where: %v", err)
	}
	if len(byWhere) != 3 {
		t.Fatalf("find by where returned %d rows, want 3", len(byWhere))
	}

	// An empty IN set must be a false predicate, not a syntax error.
	none, err := repo.FindByWhere(ctx, []repository.Cond{repository.In("name", []string{})})
	if err != nil {
		t.Fatalf("empty IN: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("empty IN returned %d rows, want 0", len(none))
	}

	if err := repo.DeleteOne(ctx, repository.Filter{"email": "user0@example.com"}); err != nil {
		t.Fatalf("delete one: %v", err)
	}
	count, err := repo.CountByCondition(ctx, nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
}

func TestUpsertIntegration(t *testing.T) {
	db, d := openTestDB(t)
	createUsersTable(t, db, d)

	repo := repository.NewBaseRepositoryWithDialect[user, int64](db, d, "users", "id", true)
	ctx := context.Background()

	if _, err := repo.Upsert(ctx, &user{Name: "Ada", Email: "ada@example.com", Age: 36}, "email"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if _, err := repo.Upsert(ctx, &user{Name: "Ada Lovelace", Email: "ada@example.com", Age: 37}, "email"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	count, err := repo.CountByCondition(ctx, repository.Filter{"email": "ada@example.com"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("upsert produced %d rows, want 1", count)
	}

	found, err := repo.FindOne(ctx, repository.Filter{"email": "ada@example.com"})
	if err != nil || found == nil {
		t.Fatalf("find = %+v, %v", found, err)
	}
	if found.Age != 37 {
		t.Fatalf("age = %d, want 37 after the conflicting upsert", found.Age)
	}
}

func TestTransactionIsolationOption(t *testing.T) {
	db, d := openTestDB(t)
	createUsersTable(t, db, d)

	repo := repository.NewBaseRepositoryWithDialect[user, int64](db, d, "users", "id", true)
	ctx := context.Background()

	err := repository.WithTransactionOpts(ctx, db,
		&sql.TxOptions{Isolation: sql.LevelRepeatableRead},
		func(tx *sql.Tx) error {
			_, err := repo.CreateTx(ctx, tx, &user{Name: "Grace", Email: "grace@example.com", Age: 45})
			return err
		})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	found, err := repo.FindOne(ctx, repository.Filter{"email": "grace@example.com"})
	if err != nil || found == nil {
		t.Fatalf("committed row not found: %+v, %v", found, err)
	}

	// A returned error must roll the whole thing back.
	sentinel := errors.New("boom")
	err = repository.WithTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := repo.CreateTx(ctx, tx, &user{Name: "Rolled", Email: "rolled@example.com"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTransaction = %v, want the sentinel", err)
	}
	rolled, err := repo.FindOne(ctx, repository.Filter{"email": "rolled@example.com"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if rolled != nil {
		t.Fatal("row survived a rolled-back transaction")
	}
}
