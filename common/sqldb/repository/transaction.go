package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTransaction executes fn within a database transaction, committing on a nil
// error and rolling back otherwise. A panic inside fn rolls back and re-panics
// so the original stack survives.
//
// The tx passed to fn is valid only for the duration of the call. database/sql
// finalises it on Commit or Rollback, and every later use returns
// sql.ErrTxDone — so never store it in a struct, close over it in a goroutine
// that outlives fn, or return it.
//
// Uses the driver's default isolation level; see WithTransactionOpts when the
// operation is a read-modify-write that needs a stronger one.
func WithTransaction(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	return WithTransactionOpts(ctx, db, nil, fn)
}

// WithTransactionOpts is WithTransaction with explicit sql.TxOptions.
//
// The isolation level matters for correctness, not just performance: a
// read-then-write sequence under READ COMMITTED (the default on PostgreSQL and
// MySQL) can interleave with a concurrent writer and lose an update. Pass
// &sql.TxOptions{Isolation: sql.LevelRepeatableRead} — or Serializable — for
// those, and ReadOnly: true for reporting queries so the engine can optimise and
// reject accidental writes.
func WithTransactionOpts(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	if db == nil {
		return fmt.Errorf("transaction: db is nil")
	}
	if fn == nil {
		return fmt.Errorf("transaction: fn is nil")
	}

	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction error: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p) // re-throw after rollback
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback error: %w (original: %v)", rbErr, err)
		}
		return err
	}

	// A failed Commit needs no Rollback: database/sql closes the Tx and releases
	// the connection whether Commit succeeded or not, so a follow-up Rollback
	// would only return sql.ErrTxDone.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit error: %w", err)
	}

	return nil
}

// WithTransactionResult executes fn within a transaction and returns its result
// alongside the error. The same tx-lifetime rule as WithTransaction applies.
func WithTransactionResult[T any](ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) (T, error)) (T, error) {
	return WithTransactionResultOpts(ctx, db, nil, fn)
}

// WithTransactionResultOpts is WithTransactionResult with explicit
// sql.TxOptions. See WithTransactionOpts for why the isolation level matters.
func WithTransactionResultOpts[T any](
	ctx context.Context,
	db *sql.DB,
	opts *sql.TxOptions,
	fn func(tx *sql.Tx) (T, error),
) (T, error) {
	var result T

	if db == nil {
		return result, fmt.Errorf("transaction: db is nil")
	}
	if fn == nil {
		return result, fmt.Errorf("transaction: fn is nil")
	}

	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return result, fmt.Errorf("begin transaction error: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	result, err = fn(tx)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return result, fmt.Errorf("rollback error: %w (original: %v)", rbErr, err)
		}
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit error: %w", err)
	}

	return result, nil
}
