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
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/sqldb"
	"github.com/nika-framework/nika/common/sqldb/repository"
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

	// Checksum fingerprints the migration body. LoadFS fills it with the SHA-256
	// of the .sql files; Go-coded migrations may set it by hand or leave it
	// empty. When set, the runner refuses to proceed if the recorded checksum
	// differs — an already-applied migration whose text changed means the
	// database no longer matches the source that supposedly built it, and every
	// later migration is written against the wrong assumption.
	Checksum string
}

// Applied is one row from the tracking table.
type Applied struct {
	Version   int64
	Name      string
	AppliedAt time.Time
	Checksum  string
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

// ErrChecksumMismatch reports that an applied migration's body changed since it
// ran.
var ErrChecksumMismatch = errors.New("migration: checksum mismatch")

// Migrator applies migrations against a connected sqldb.DB.
type Migrator struct {
	db          *sqldb.DB
	table       string
	quotedTable string
	migs        []*Migration
	dialect     sqldb.Driver
	lockTimeout time.Duration

	ensureOnce sync.Once
	ensureErr  error
}

// dialectFor maps the connection driver to the repository dialect used for
// identifier quoting.
func dialectFor(d sqldb.Driver) repository.Dialect {
	switch d {
	case sqldb.DriverMySQL:
		return repository.DialectMySQL
	case sqldb.DriverSQLite:
		return repository.DialectSQLite
	default:
		return repository.DialectPostgres
	}
}

// quoteTable validates and quotes a tracking-table name. The name is
// interpolated into DDL that cannot be parameterised, so it panics on anything
// that is not a plain identifier — a bad table name is a configuration error that
// must fail at startup.
func quoteTable(driver sqldb.Driver, table string) string {
	d := dialectFor(driver)
	if err := repository.ValidateIdentifier(table); err != nil {
		panic(fmt.Sprintf("nika/migration: invalid tracking table %q: %v", table, err))
	}
	return d.QuoteQualified(table)
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
	driver := db.Driver()
	return &Migrator{
		db:          db,
		table:       "schema_migrations",
		quotedTable: quoteTable(driver, "schema_migrations"),
		migs:        sorted,
		dialect:     driver,
		lockTimeout: defaultLockTimeout,
	}
}

// WithTable overrides the tracking table name (default schema_migrations).
// It panics when name is not a plain SQL identifier.
func (m *Migrator) WithTable(name string) *Migrator {
	if name != "" {
		m.quotedTable = quoteTable(m.dialect, name)
		m.table = name
		m.ensureOnce = sync.Once{}
	}
	return m
}

// WithLockTimeout bounds how long Up/Down wait for the cluster-wide migration
// lock before failing.
func (m *Migrator) WithLockTimeout(d time.Duration) *Migrator {
	if d > 0 {
		m.lockTimeout = d
	}
	return m
}

// Ensure creates the tracking table if it does not exist.
func (m *Migrator) Ensure(ctx context.Context) error {
	// Ensure is called from Applied, which every other method calls; doing the
	// DDL once per Migrator keeps that off the hot path.
	m.ensureOnce.Do(func() {
		m.ensureErr = m.ensure(ctx)
	})
	return m.ensureErr
}

func (m *Migrator) ensure(ctx context.Context) error {
	var stmt string
	switch m.dialect {
	case sqldb.DriverPostgres:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL DEFAULT '',
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`, m.quotedTable)
	case sqldb.DriverMySQL:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version BIGINT PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    checksum VARCHAR(64) NOT NULL DEFAULT '',
    applied_at DATETIME(6) NOT NULL
)`, m.quotedTable)
	default:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL DEFAULT '',
    applied_at DATETIME NOT NULL
)`, m.quotedTable)
	}
	if _, err := m.db.Conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("migration ensure: %w", err)
	}

	// A tracking table created before checksums existed has no checksum column.
	// The ALTER fails with "duplicate column" on an up-to-date table, which is
	// the expected case and not an error worth surfacing.
	_, _ = m.db.Conn.ExecContext(ctx,
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN checksum %s", m.quotedTable, m.checksumColumnType()))

	return nil
}

func (m *Migrator) checksumColumnType() string {
	if m.dialect == sqldb.DriverMySQL {
		return "VARCHAR(64) NOT NULL DEFAULT ''"
	}
	return "TEXT NOT NULL DEFAULT ''"
}

// Applied lists the migrations already recorded, ordered by version ASC.
func (m *Migrator) Applied(ctx context.Context) ([]Applied, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	rows, err := m.db.Conn.QueryContext(ctx,
		fmt.Sprintf("SELECT version, name, applied_at, checksum FROM %s ORDER BY version ASC", m.quotedTable))
	if err != nil {
		return nil, fmt.Errorf("migration query: %w", err)
	}
	defer rows.Close()

	var out []Applied
	for rows.Next() {
		var a Applied
		var checksum sql.NullString
		if err := rows.Scan(&a.Version, &a.Name, &a.AppliedAt, &checksum); err != nil {
			return nil, fmt.Errorf("migration scan: %w", err)
		}
		a.Checksum = checksum.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// Verify reports whether every already-applied migration still matches the
// checksum recorded when it ran. Migrations with no checksum on either side are
// skipped, so hand-written Go migrations keep working.
func (m *Migrator) Verify(ctx context.Context) error {
	applied, err := m.Applied(ctx)
	if err != nil {
		return err
	}
	recorded := make(map[int64]Applied, len(applied))
	for _, a := range applied {
		recorded[a.Version] = a
	}

	for _, mg := range m.migs {
		a, ok := recorded[mg.Version]
		if !ok || mg.Checksum == "" || a.Checksum == "" {
			continue
		}
		if a.Checksum != mg.Checksum {
			return fmt.Errorf("%w: migration %d %q was applied as %s but is now %s",
				ErrChecksumMismatch, mg.Version, mg.Name, a.Checksum, mg.Checksum)
		}
	}
	return nil
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
//
// The whole run is serialised by a cluster-wide advisory lock, and the pending
// set is recomputed after the lock is held: two pods starting together must not
// both apply the same version.
func (m *Migrator) UpN(ctx context.Context, n int) ([]int64, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}

	lock, err := m.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.release(ctx) }()

	// Refusing to run on a changed history is the point of the checksum: the
	// database no longer matches the source it was built from.
	if err := m.Verify(ctx); err != nil {
		return nil, err
	}

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
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}

	lock, err := m.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.release(ctx) }()

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

	// Claim the version *before* running the body. The primary key on version
	// then serialises concurrent runners even if the advisory lock is
	// unavailable: the loser blocks on the row lock and then fails with a
	// duplicate key instead of re-running the migration.
	//
	// Caveat on MySQL: DDL implicitly commits, so a migration that fails after
	// its first DDL statement leaves the claim committed and must be cleaned up
	// by hand. That is inherent to MySQL, not to this protocol.
	insert := insertAppliedStmt(m.dialect, m.quotedTable)
	if _, err := tx.ExecContext(ctx, insert, mig.Version, mig.Name, mig.Checksum, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record applied: %w", err)
	}

	if err := mig.Up(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
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

	del := deleteAppliedStmt(m.dialect, m.quotedTable)
	if _, err := tx.ExecContext(ctx, del, mig.Version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete applied: %w", err)
	}
	return tx.Commit()
}

// quotedTable is already validated and quoted by quoteTable; the column names
// here are literals in this file.
func insertAppliedStmt(d sqldb.Driver, quotedTable string) string {
	if d == sqldb.DriverPostgres {
		return fmt.Sprintf("INSERT INTO %s (version, name, checksum, applied_at) VALUES ($1, $2, $3, $4)", quotedTable)
	}
	return fmt.Sprintf("INSERT INTO %s (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", quotedTable)
}

func deleteAppliedStmt(d sqldb.Driver, quotedTable string) string {
	if d == sqldb.DriverPostgres {
		return fmt.Sprintf("DELETE FROM %s WHERE version = $1", quotedTable)
	}
	return fmt.Sprintf("DELETE FROM %s WHERE version = ?", quotedTable)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
