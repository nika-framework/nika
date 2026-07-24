// Package migration provides a lightweight, production-grade migration
// runner for the nika SQL layer. Migrations are versioned (typically by
// UTC timestamp) and applied in ascending order. Each applied migration is
// recorded in a tracking table so re-running the migrator is idempotent.
//
// Migrations can be authored two ways:
//   - As Go code via the Register() function (typed, testable).
//   - As pairs of .sql files loaded from a fs.FS via LoadFS().
//
// Both approaches share the same runner, tracking table, and CLI.
package migration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/sqldb"
)

// UpFn runs the forward migration inside an open transaction.
type UpFn func(ctx context.Context, tx *sql.Tx) error

// DownFn runs the rollback inside an open transaction.
type DownFn func(ctx context.Context, tx *sql.Tx) error

// Migration is a single forward+rollback unit identified by Version.
// Version conventionally follows YYYYMMDDHHMMSS to keep chronological order,
// but any monotonically increasing int64 is fine.
type Migration struct {
	Version int64
	Name    string
	Up      UpFn
	Down    DownFn
}

// Applied is one row from the tracking table.
type Applied struct {
	Version   int64
	Name      string
	AppliedAt time.Time
}

var (
	registryMu sync.Mutex
	registry   []*Migration
)

// Register adds a migration to the process-wide registry. Call from an
// init() so the migration self-installs when its package is imported.
// Duplicate versions panic — they indicate a copy/paste bug.
func Register(m *Migration) {
	if m == nil {
		panic("migration: cannot register nil migration")
	}
	if m.Version <= 0 {
		panic(fmt.Sprintf("migration: version must be positive (got %d)", m.Version))
	}
	if m.Up == nil {
		panic(fmt.Sprintf("migration %d: Up is required", m.Version))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registry {
		if existing.Version == m.Version {
			panic(fmt.Sprintf("migration: duplicate version %d (%q vs %q)",
				m.Version, existing.Name, m.Name))
		}
	}
	registry = append(registry, m)
}

// Registered returns a snapshot of all registered migrations sorted by version.
func Registered() []*Migration {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]*Migration, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// Reset clears the process-wide registry. Only useful in tests.
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}

// Migrator applies migrations against a connected sqldb.DB.
type Migrator struct {
	db      *sqldb.DB
	table   string
	migs    []*Migration
	dialect sqldb.Driver
}

// New creates a Migrator using the migrations currently in the registry.
func New(db *sqldb.DB) *Migrator {
	return NewWith(db, Registered())
}

// NewWith creates a Migrator bound to a specific list of migrations.
// The list is copied and re-sorted by version.
func NewWith(db *sqldb.DB, migs []*Migration) *Migrator {
	sorted := make([]*Migration, len(migs))
	copy(sorted, migs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	return &Migrator{
		db:      db,
		table:   "schema_migrations",
		migs:    sorted,
		dialect: db.Driver(),
	}
}

// WithTable overrides the tracking table name (default schema_migrations).
func (m *Migrator) WithTable(name string) *Migrator {
	if name != "" {
		m.table = name
	}
	return m
}

// Ensure creates the tracking table if it does not exist.
func (m *Migrator) Ensure(ctx context.Context) error {
	var stmt string
	switch m.dialect {
	case sqldb.DriverPostgres:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`, m.table)
	case sqldb.DriverMySQL:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version BIGINT PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    applied_at DATETIME(6) NOT NULL
)`, m.table)
	default:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at DATETIME NOT NULL
)`, m.table)
	}
	_, err := m.db.Conn.ExecContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("migration ensure: %w", err)
	}
	return nil
}

// Applied lists the migrations already recorded, ordered by version ASC.
func (m *Migrator) Applied(ctx context.Context) ([]Applied, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	rows, err := m.db.Conn.QueryContext(ctx,
		fmt.Sprintf("SELECT version, name, applied_at FROM %s ORDER BY version ASC", m.table))
	if err != nil {
		return nil, fmt.Errorf("migration query: %w", err)
	}
	defer rows.Close()

	var out []Applied
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Name, &a.AppliedAt); err != nil {
			return nil, fmt.Errorf("migration scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Pending returns migrations that have not yet been applied.
func (m *Migrator) Pending(ctx context.Context) ([]*Migration, error) {
	applied, err := m.Applied(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(applied))
	for _, a := range applied {
		seen[a.Version] = struct{}{}
	}
	var pending []*Migration
	for _, mg := range m.migs {
		if _, ok := seen[mg.Version]; !ok {
			pending = append(pending, mg)
		}
	}
	return pending, nil
}

// Up applies every pending migration in order.
func (m *Migrator) Up(ctx context.Context) ([]int64, error) {
	return m.UpN(ctx, 0)
}

// UpN applies at most n pending migrations (n <= 0 means "all").
func (m *Migrator) UpN(ctx context.Context, n int) ([]int64, error) {
	pending, err := m.Pending(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 && n < len(pending) {
		pending = pending[:n]
	}

	applied := make([]int64, 0, len(pending))
	for _, mig := range pending {
		if err := m.applyOne(ctx, mig); err != nil {
			return applied, fmt.Errorf("apply migration %d %q: %w", mig.Version, mig.Name, err)
		}
		applied = append(applied, mig.Version)
	}
	return applied, nil
}

// Down rolls back the most recent applied migration.
func (m *Migrator) Down(ctx context.Context) (int64, error) {
	versions, err := m.DownN(ctx, 1)
	if err != nil || len(versions) == 0 {
		return 0, err
	}
	return versions[0], nil
}

// DownN rolls back the last n applied migrations, newest first.
func (m *Migrator) DownN(ctx context.Context, n int) ([]int64, error) {
	if n <= 0 {
		return nil, nil
	}
	applied, err := m.Applied(ctx)
	if err != nil {
		return nil, err
	}
	if len(applied) == 0 {
		return nil, nil
	}

	byVersion := make(map[int64]*Migration, len(m.migs))
	for _, mg := range m.migs {
		byVersion[mg.Version] = mg
	}

	rolled := make([]int64, 0, n)
	for i := len(applied) - 1; i >= 0 && len(rolled) < n; i-- {
		mig, ok := byVersion[applied[i].Version]
		if !ok {
			return rolled, fmt.Errorf("cannot rollback %d %q: migration not registered",
				applied[i].Version, applied[i].Name)
		}
		if mig.Down == nil {
			return rolled, fmt.Errorf("cannot rollback %d %q: no Down function",
				mig.Version, mig.Name)
		}
		if err := m.rollbackOne(ctx, mig); err != nil {
			return rolled, fmt.Errorf("rollback %d %q: %w", mig.Version, mig.Name, err)
		}
		rolled = append(rolled, mig.Version)
	}
	return rolled, nil
}

// Status returns a human-readable summary of applied vs pending migrations.
func (m *Migrator) Status(ctx context.Context) (string, error) {
	applied, err := m.Applied(ctx)
	if err != nil {
		return "", err
	}
	appliedSet := make(map[int64]Applied, len(applied))
	for _, a := range applied {
		appliedSet[a.Version] = a
	}

	var sb strings.Builder
	sb.WriteString("VERSION           NAME                          STATUS       APPLIED AT\n")
	for _, mg := range m.migs {
		if a, ok := appliedSet[mg.Version]; ok {
			fmt.Fprintf(&sb, "%-17d %-29s applied      %s\n",
				mg.Version, truncate(mg.Name, 29), a.AppliedAt.UTC().Format(time.RFC3339))
		} else {
			fmt.Fprintf(&sb, "%-17d %-29s pending      -\n",
				mg.Version, truncate(mg.Name, 29))
		}
	}
	return sb.String(), nil
}

func (m *Migrator) applyOne(ctx context.Context, mig *Migration) error {
	tx, err := m.db.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := mig.Up(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	insert := insertAppliedStmt(m.dialect, m.table)
	if _, err := tx.ExecContext(ctx, insert, mig.Version, mig.Name, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record applied: %w", err)
	}
	return tx.Commit()
}

func (m *Migrator) rollbackOne(ctx context.Context, mig *Migration) error {
	tx, err := m.db.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := mig.Down(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	del := deleteAppliedStmt(m.dialect, m.table)
	if _, err := tx.ExecContext(ctx, del, mig.Version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete applied: %w", err)
	}
	return tx.Commit()
}

func insertAppliedStmt(d sqldb.Driver, table string) string {
	if d == sqldb.DriverPostgres {
		return fmt.Sprintf("INSERT INTO %s (version, name, applied_at) VALUES ($1, $2, $3)", table)
	}
	return fmt.Sprintf("INSERT INTO %s (version, name, applied_at) VALUES (?, ?, ?)", table)
}

func deleteAppliedStmt(d sqldb.Driver, table string) string {
	if d == sqldb.DriverPostgres {
		return fmt.Sprintf("DELETE FROM %s WHERE version = $1", table)
	}
	return fmt.Sprintf("DELETE FROM %s WHERE version = ?", table)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
