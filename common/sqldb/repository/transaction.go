package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// WithTransaction executes the given function within a database transaction.
// If the function returns an error, the transaction is rolled back.
// If the function succeeds, the transaction is committed.
func WithTransaction(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
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

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit error: %w", err)
	}

	return nil
}

// WithTransactionResult executes the given function within a database transaction
// and returns a result alongside the error.
func WithTransactionResult[T any](ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) (T, error)) (T, error) {
	var result T

	tx, err := db.BeginTx(ctx, nil)
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
