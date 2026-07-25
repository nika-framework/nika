package migration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nika-framework/nika/common/sqldb"
)

func TestQuoteTable(t *testing.T) {
	cases := []struct {
		driver sqldb.Driver
		table  string
		want   string
	}{
		{sqldb.DriverPostgres, "schema_migrations", `"schema_migrations"`},
		{sqldb.DriverSQLite, "schema_migrations", `"schema_migrations"`},
		{sqldb.DriverMySQL, "schema_migrations", "`schema_migrations`"},
		{sqldb.DriverPostgres, "app.schema_migrations", `"app"."schema_migrations"`},
	}

	for _, tc := range cases {
		if got := quoteTable(tc.driver, tc.table); got != tc.want {
			t.Errorf("quoteTable(%s, %q) = %s, want %s", tc.driver, tc.table, got, tc.want)
		}
	}
}

func TestQuoteTablePanicsOnInjection(t *testing.T) {
	// The tracking table is interpolated into DDL that cannot be parameterised,
	// so a bad name has to fail loudly at startup.
	payloads := []string{
		"schema_migrations; DROP TABLE users --",
		`m" (x int); --`,
		"m`; DROP TABLE users",
		"",
		"1migrations",
		"select",
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("quoteTable(%q) did not panic", payload)
				}
			}()
			quoteTable(sqldb.DriverPostgres, payload)
		})
	}
}

func TestLockKeyIsStableAndDistinct(t *testing.T) {
	first := lockKey("schema_migrations")
	if first != lockKey("schema_migrations") {
		t.Error("lockKey is not deterministic")
	}
	if first == lockKey("other_migrations") {
		t.Error("different tracking tables must not share a lock key")
	}
	// pg_advisory_lock takes a signed bigint; a negative key is legal but makes
	// pg_locks output confusing, so the sign bit is cleared.
	if first < 0 {
		t.Errorf("lockKey = %d, want a non-negative value", first)
	}
}

func TestInsertAppliedStmt(t *testing.T) {
	cases := []struct {
		driver sqldb.Driver
		want   string
	}{
		{sqldb.DriverPostgres, `INSERT INTO "m" (version, name, checksum, applied_at) VALUES ($1, $2, $3, $4)`},
		{sqldb.DriverMySQL, "INSERT INTO `m` (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)"},
		{sqldb.DriverSQLite, `INSERT INTO "m" (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`},
	}

	for _, tc := range cases {
		quoted := quoteTable(tc.driver, "m")
		if got := insertAppliedStmt(tc.driver, quoted); got != tc.want {
			t.Errorf("insertAppliedStmt(%s)\n got: %s\nwant: %s", tc.driver, got, tc.want)
		}
	}
}

func TestDeleteAppliedStmt(t *testing.T) {
	if got := deleteAppliedStmt(sqldb.DriverPostgres, `"m"`); got != `DELETE FROM "m" WHERE version = $1` {
		t.Errorf("postgres delete = %s", got)
	}
	if got := deleteAppliedStmt(sqldb.DriverMySQL, "`m`"); got != "DELETE FROM `m` WHERE version = ?" {
		t.Errorf("mysql delete = %s", got)
	}
}

func TestLoadFSComputesChecksums(t *testing.T) {
	fs := fstest.MapFS{
		"migrations/20240101000000_create_users.up.sql":   {Data: []byte("CREATE TABLE users (id INT);")},
		"migrations/20240101000000_create_users.down.sql": {Data: []byte("DROP TABLE users;")},
		"migrations/20240102000000_add_email.up.sql":      {Data: []byte("ALTER TABLE users ADD email TEXT;")},
		"migrations/notes.txt":                            {Data: []byte("ignored")},
	}

	migs, err := LoadFS(fs, "migrations")
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("loaded %d migrations, want 2", len(migs))
	}
	if migs[0].Version != 20240101000000 || migs[1].Version != 20240102000000 {
		t.Errorf("versions out of order: %d, %d", migs[0].Version, migs[1].Version)
	}
	for _, m := range migs {
		// SHA-256 hex.
		if len(m.Checksum) != 64 {
			t.Errorf("migration %d checksum = %q, want a 64-char hex digest", m.Version, m.Checksum)
		}
	}
	if migs[0].Checksum == migs[1].Checksum {
		t.Error("different migration bodies produced the same checksum")
	}
	if migs[0].Down == nil {
		t.Error("a .down.sql file should produce a Down function")
	}
	if migs[1].Down != nil {
		t.Error("a migration with no .down.sql should have a nil Down")
	}
}

func TestFileChecksumDetectsEdits(t *testing.T) {
	base := fileChecksum("CREATE TABLE t (id INT);", "DROP TABLE t;")

	if base != fileChecksum("CREATE TABLE t (id INT);", "DROP TABLE t;") {
		t.Error("fileChecksum is not deterministic")
	}
	if base == fileChecksum("CREATE TABLE t (id BIGINT);", "DROP TABLE t;") {
		t.Error("an edited up migration must change the checksum")
	}
	// An edited rollback is just as much a divergence from what the database was
	// built with.
	if base == fileChecksum("CREATE TABLE t (id INT);", "DROP TABLE IF EXISTS t;") {
		t.Error("an edited down migration must change the checksum")
	}
	// The separator prevents up/down concatenation collisions.
	if fileChecksum("ab", "c") == fileChecksum("a", "bc") {
		t.Error("checksum is ambiguous across the up/down boundary")
	}
}

func TestLoadFSRejectsMissingUp(t *testing.T) {
	fs := fstest.MapFS{
		"migrations/20240101000000_x.down.sql": {Data: []byte("DROP TABLE x;")},
	}
	_, err := LoadFS(fs, "migrations")
	if err == nil || !strings.Contains(err.Error(), "missing .up.sql") {
		t.Fatalf("LoadFS = %v, want a missing-up error", err)
	}
}

func TestRegisterRejectsBadMigrations(t *testing.T) {
	t.Cleanup(Reset)

	assertPanics(t, "nil migration", func() { Register(nil) })
	assertPanics(t, "zero version", func() { Register(&Migration{Version: 0}) })
	assertPanics(t, "missing Up", func() { Register(&Migration{Version: 1}) })

	Reset()
	Register(&Migration{Version: 1, Name: "a", Up: noopUp})
	assertPanics(t, "duplicate version", func() {
		Register(&Migration{Version: 1, Name: "b", Up: noopUp})
	})

	if got := Registered(); len(got) != 1 {
		t.Fatalf("Registered() has %d entries, want 1", len(got))
	}
}

func noopUp(context.Context, *sql.Tx) error { return nil }

func assertPanics(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}
