package migration

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/nika-framework/nika/common/sqldb"
)

// defaultLockTimeout bounds how long a process waits for a peer to finish
// migrating before giving up. Long enough for a slow index build, short enough
// that a stuck pod does not block a rollout forever.
const defaultLockTimeout = 5 * time.Minute

// advisoryLock is a cluster-wide mutex held for the duration of a migration run.
//
// Two pods rolling out at once both see the same pending set and both try to
// apply it. The tracking-table primary key stops the *record* from being written
// twice, but not the migration body: two concurrent `CREATE INDEX` or two
// backfills would still run, and on MySQL — where DDL implicitly commits — the
// second one fails halfway with the tracking table already updated.
//
// The lock must be held on a single dedicated connection: both PostgreSQL
// advisory locks and MySQL's GET_LOCK are session-scoped, so acquiring on a
// pooled *sql.DB could release from a different session than it locked.
type advisoryLock struct {
	conn    *sql.Conn
	driver  sqldb.Driver
	key     int64
	name    string
	deleted bool
}

// lockKey derives a stable 63-bit key from the tracking table name so different
// tracking tables (e.g. per-service schemas on one server) do not block each
// other.
func lockKey(table string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("nika:migration:" + table))
	// Clear the sign bit: pg_advisory_lock takes a signed bigint and a negative
	// key is legal but confusing in pg_locks output.
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// acquireLock takes the migration lock, blocking up to timeout.
// SQLite gets a no-op: it serialises writers with a file lock, and a shared
// SQLite file across pods is not a supported deployment anyway.
func (m *Migrator) acquireLock(ctx context.Context) (*advisoryLock, error) {
	if m.dialect == sqldb.DriverSQLite {
		return nil, nil
	}

	timeout := m.lockTimeout
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}

	conn, err := m.db.Conn.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration lock: acquire connection: %w", err)
	}

	lock := &advisoryLock{
		conn:   conn,
		driver: m.dialect,
		key:    lockKey(m.table),
		name:   "nika_migration_" + m.table,
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch m.dialect {
	case sqldb.DriverPostgres:
		// pg_advisory_lock blocks until granted; the context timeout is what
		// bounds the wait.
		if _, err := conn.ExecContext(waitCtx, "SELECT pg_advisory_lock($1)", lock.key); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("migration lock: pg_advisory_lock: %w", err)
		}
	case sqldb.DriverMySQL:
		var granted sql.NullInt64
		row := conn.QueryRowContext(waitCtx, "SELECT GET_LOCK(?, ?)", lock.name, int(timeout.Seconds()))
		if err := row.Scan(&granted); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("migration lock: GET_LOCK: %w", err)
		}
		// 0 means the wait timed out, NULL means an error occurred.
		if !granted.Valid || granted.Int64 != 1 {
			_ = conn.Close()
			return nil, fmt.Errorf("migration lock: another process holds %q (waited %s)", lock.name, timeout)
		}
	}

	return lock, nil
}

// release drops the lock and returns the dedicated connection to the pool.
func (l *advisoryLock) release(ctx context.Context) error {
	if l == nil || l.deleted {
		return nil
	}
	l.deleted = true

	var err error
	switch l.driver {
	case sqldb.DriverPostgres:
		_, err = l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", l.key)
	case sqldb.DriverMySQL:
		_, err = l.conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", l.name)
	}

	if closeErr := l.conn.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("migration unlock: %w", err)
	}
	return nil
}
