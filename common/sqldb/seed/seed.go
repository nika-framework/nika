// Package seed runs idempotent data-seed jobs against a nika SQL connection.
//
// A seed is registered by name and, once applied, is recorded in a tracking
// table so subsequent runs skip it. Seeds intended to re-run every time
// (e.g. syncing a reference dataset from configuration) can set AlwaysRun.
package seed

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/sqldb"
	"github.com/nika-framework/nika/common/sqldb/repository"
)

// RunFn executes the seed inside an open transaction.
type RunFn func(ctx context.Context, tx *sql.Tx) error

// Seed is one named data-seed operation.
type Seed struct {
	// Name uniquely identifies the seed within the project.
	Name string
	// Order controls execution order (ascending). Seeds with equal Order
	// fall back to Name to keep the sequence deterministic.
	Order int
	// Run performs the seeding logic.
	Run RunFn
	// AlwaysRun re-executes on every seeder run even if already applied.
	AlwaysRun bool
}

var (
	registryMu sync.Mutex
	registry   []*Seed
)

// Register adds a seed to the process-wide registry.
func Register(s *Seed) {
	if s == nil {
		panic("seed: cannot register nil seed")
	}
	if s.Name == "" {
		panic("seed: Name is required")
	}
	if s.Run == nil {
		panic(fmt.Sprintf("seed %q: Run is required", s.Name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registry {
		if existing.Name == s.Name {
			panic(fmt.Sprintf("seed: duplicate name %q", s.Name))
		}
	}
	registry = append(registry, s)
}

// Registered returns a snapshot of registered seeds sorted by (Order, Name).
func Registered() []*Seed {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]*Seed, len(registry))
	copy(out, registry)
	sortSeeds(out)
	return out
}

// Reset clears the registry (for tests).
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}

func sortSeeds(s []*Seed) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Order != s[j].Order {
			return s[i].Order < s[j].Order
		}
		return s[i].Name < s[j].Name
	})
}

// Seeder applies seeds against a sqldb.DB.
type Seeder struct {
	db          *sqldb.DB
	table       string
	quotedTable string
	seeds       []*Seed
	dialect     sqldb.Driver
}

// quoteTable validates and quotes the tracking-table name. It is interpolated
// into DDL that cannot be parameterised, so anything other than a plain
// identifier panics at startup rather than reaching the database.
func quoteTable(driver sqldb.Driver, table string) string {
	var d repository.Dialect
	switch driver {
	case sqldb.DriverMySQL:
		d = repository.DialectMySQL
	case sqldb.DriverSQLite:
		d = repository.DialectSQLite
	default:
		d = repository.DialectPostgres
	}
	if err := repository.ValidateIdentifier(table); err != nil {
		panic(fmt.Sprintf("nika/seed: invalid tracking table %q: %v", table, err))
	}
	return d.QuoteQualified(table)
}

// New returns a Seeder using the current process-wide registry.
func New(db *sqldb.DB) *Seeder {
	return NewWith(db, Registered())
}

// NewWith returns a Seeder bound to an explicit list of seeds.
func NewWith(db *sqldb.DB, seeds []*Seed) *Seeder {
	sorted := make([]*Seed, len(seeds))
	copy(sorted, seeds)
	sortSeeds(sorted)
	driver := db.Driver()
	return &Seeder{
		db:          db,
		table:       "schema_seeds",
		quotedTable: quoteTable(driver, "schema_seeds"),
		seeds:       sorted,
		dialect:     driver,
	}
}

// WithTable overrides the tracking table.
// It panics when name is not a plain SQL identifier.
func (s *Seeder) WithTable(name string) *Seeder {
	if name != "" {
		s.quotedTable = quoteTable(s.dialect, name)
		s.table = name
	}
	return s
}

// Ensure creates the tracking table if missing.
func (s *Seeder) Ensure(ctx context.Context) error {
	var stmt string
	switch s.dialect {
	case sqldb.DriverPostgres:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    name TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`, s.quotedTable)
	case sqldb.DriverMySQL:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    name VARCHAR(255) PRIMARY KEY,
    applied_at DATETIME(6) NOT NULL
)`, s.quotedTable)
	default:
		stmt = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    name TEXT PRIMARY KEY,
    applied_at DATETIME NOT NULL
)`, s.quotedTable)
	}
	_, err := s.db.Conn.ExecContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("seed ensure: %w", err)
	}
	return nil
}

// AppliedNames returns the names of seeds already recorded.
func (s *Seeder) AppliedNames(ctx context.Context) (map[string]struct{}, error) {
	if err := s.Ensure(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.Conn.QueryContext(ctx,
		fmt.Sprintf("SELECT name FROM %s", s.quotedTable))
	if err != nil {
		return nil, fmt.Errorf("seed query: %w", err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("seed scan: %w", err)
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// Run executes all pending seeds (plus any marked AlwaysRun).
// It returns the names of seeds that ran.
func (s *Seeder) Run(ctx context.Context) ([]string, error) {
	applied, err := s.AppliedNames(ctx)
	if err != nil {
		return nil, err
	}
	ran := make([]string, 0, len(s.seeds))
	for _, seed := range s.seeds {
		if _, ok := applied[seed.Name]; ok && !seed.AlwaysRun {
			continue
		}
		// A seed meant to run once claims its name exclusively, so a second
		// process racing this one fails instead of duplicating the data.
		if err := s.runOne(ctx, seed, !seed.AlwaysRun); err != nil {
			return ran, fmt.Errorf("seed %q: %w", seed.Name, err)
		}
		ran = append(ran, seed.Name)
	}
	return ran, nil
}

// RunOnly executes only the named seeds regardless of prior state.
func (s *Seeder) RunOnly(ctx context.Context, names ...string) ([]string, error) {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	ran := make([]string, 0, len(names))
	for _, seed := range s.seeds {
		if _, ok := want[seed.Name]; !ok {
			continue
		}
		// RunOnly is documented to run regardless of prior state, so it upserts
		// the tracking row rather than claiming it exclusively.
		if err := s.runOne(ctx, seed, false); err != nil {
			return ran, fmt.Errorf("seed %q: %w", seed.Name, err)
		}
		ran = append(ran, seed.Name)
	}
	return ran, nil
}

func (s *Seeder) runOne(ctx context.Context, seed *Seed, exclusive bool) error {
	tx, err := s.db.Conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	// Claim the seed name *before* running it. With a plain INSERT the primary
	// key on name makes concurrent seeders mutually exclusive: the loser blocks
	// on the row lock and then fails with a duplicate key, instead of inserting
	// the same reference data a second time. Recording afterwards, as this used
	// to, gave both processes a clear run at the body.
	claim := insertAppliedStmt(s.dialect, s.quotedTable)
	if !exclusive {
		claim = upsertAppliedStmt(s.dialect, s.quotedTable)
	}
	if _, err := tx.ExecContext(ctx, claim, seed.Name, time.Now().UTC()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record applied: %w", err)
	}

	if err := seed.Run(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// insertAppliedStmt claims a seed name exclusively — it fails if the name is
// already recorded.
func insertAppliedStmt(d sqldb.Driver, quotedTable string) string {
	if d == sqldb.DriverPostgres {
		return fmt.Sprintf("INSERT INTO %s (name, applied_at) VALUES ($1, $2)", quotedTable)
	}
	return fmt.Sprintf("INSERT INTO %s (name, applied_at) VALUES (?, ?)", quotedTable)
}

// quotedTable is validated and quoted by quoteTable; column names here are
// literals in this file.
func upsertAppliedStmt(d sqldb.Driver, quotedTable string) string {
	switch d {
	case sqldb.DriverPostgres:
		return fmt.Sprintf(
			"INSERT INTO %s (name, applied_at) VALUES ($1, $2) ON CONFLICT (name) DO UPDATE SET applied_at = EXCLUDED.applied_at",
			quotedTable)
	case sqldb.DriverMySQL:
		return fmt.Sprintf(
			"INSERT INTO %s (name, applied_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE applied_at = VALUES(applied_at)",
			quotedTable)
	default:
		return fmt.Sprintf(
			"INSERT INTO %s (name, applied_at) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET applied_at = excluded.applied_at",
			quotedTable)
	}
}
