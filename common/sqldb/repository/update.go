package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UpdateOneByID updates a single record by its primary key.
func (r *BaseRepository[T, ID]) UpdateOneByID(ctx context.Context, id ID, data Filter) error {
	setClause, setArgs := r.buildSetClause(data, 0)

	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s = $%d",
		r.TableName,
		setClause,
		r.IDColumn,
		len(setArgs)+1,
	)

	args := append(setArgs, id)
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// UpdateOne updates the first record matching the filter.
// Uses a subquery with ctid (Postgres) or LIMIT 1 to restrict the update.
func (r *BaseRepository[T, ID]) UpdateOne(ctx context.Context, filter Filter, data Filter) error {
	setClause, setArgs := r.buildSetClause(data, 0)
	whereClause, whereArgs := r.buildWhere(filter, len(setArgs))

	// Use subquery to update only one row
	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s IN (SELECT %s FROM %s %s LIMIT 1)",
		r.TableName,
		setClause,
		r.IDColumn,
		r.IDColumn,
		r.TableName,
		whereClause,
	)

	args := append(setArgs, whereArgs...)
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// UpdateAndFindOne updates a record and returns the updated version.
func (r *BaseRepository[T, ID]) UpdateAndFindOne(ctx context.Context, filter Filter, data Filter) (*T, error) {
	setClause, setArgs := r.buildSetClause(data, 0)
	whereClause, whereArgs := r.buildWhere(filter, len(setArgs))

	query := fmt.Sprintf(
		"UPDATE %s SET %s %s RETURNING %s",
		r.TableName,
		setClause,
		whereClause,
		r.columnsString(),
	)

	args := append(setArgs, whereArgs...)
	row := r.DB.QueryRowContext(ctx, query, args...)
	return r.scanRow(row)
}

// UpdateMany updates all records matching the filter. Returns the number of affected rows.
func (r *BaseRepository[T, ID]) UpdateMany(ctx context.Context, filter Filter, data Filter) (int64, error) {
	setClause, setArgs := r.buildSetClause(data, 0)
	whereClause, whereArgs := r.buildWhere(filter, len(setArgs))

	query := fmt.Sprintf(
		"UPDATE %s SET %s %s",
		r.TableName,
		setClause,
		whereClause,
	)

	args := append(setArgs, whereArgs...)
	result, err := r.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// Increment increases a numeric column by the given value.
func (r *BaseRepository[T, ID]) Increment(ctx context.Context, filter Filter, column string, value int64) error {
	if value < 1 {
		return errors.New("increment value must be positive")
	}

	whereClause, whereArgs := r.buildWhere(filter, 1)

	query := fmt.Sprintf(
		"UPDATE %s SET %s = %s + $1 %s",
		r.TableName,
		column,
		column,
		whereClause,
	)

	args := append([]any{value}, whereArgs...)
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// Decrement decreases a numeric column by the given value.
func (r *BaseRepository[T, ID]) Decrement(ctx context.Context, filter Filter, column string, value int64) error {
	if value < 1 {
		return errors.New("decrement value must be positive")
	}

	whereClause, whereArgs := r.buildWhere(filter, 1)

	query := fmt.Sprintf(
		"UPDATE %s SET %s = %s - $1 %s",
		r.TableName,
		column,
		column,
		whereClause,
	)

	args := append([]any{value}, whereArgs...)
	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// RawExec executes a raw SQL statement (INSERT, UPDATE, DELETE).
func (r *BaseRepository[T, ID]) RawExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.DB.ExecContext(ctx, query, args...)
}

// Upsert performs an INSERT ... ON CONFLICT ... DO UPDATE (Postgres).
// conflictColumns: columns that form the unique constraint.
// data: the record to upsert.
func (r *BaseRepository[T, ID]) Upsert(ctx context.Context, data *T, conflictColumns ...string) (*T, error) {
	cols := r.insertCols
	values := r.getStructValues(data, cols)

	// Build the ON CONFLICT UPDATE clause
	updateParts := make([]string, 0, len(cols))
	for _, col := range cols {
		// Skip conflict columns in the update part
		isConflict := false
		for _, cc := range conflictColumns {
			if col == cc {
				isConflict = true
				break
			}
		}
		if !isConflict {
			updateParts = append(updateParts, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
		}
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s RETURNING %s",
		r.TableName,
		strings.Join(cols, ", "),
		placeholders(1, len(cols)),
		strings.Join(conflictColumns, ", "),
		strings.Join(updateParts, ", "),
		r.columnsString(),
	)

	row := r.DB.QueryRowContext(ctx, query, values...)
	return r.scanRow(row)
}
