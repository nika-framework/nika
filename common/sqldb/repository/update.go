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
		"UPDATE %s SET %s WHERE %s = %s",
		r.TableName,
		setClause,
		r.IDColumn,
		r.Dialect.placeholder(len(setArgs)+1),
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
	if r.Dialect == DialectMySQL {
		query := fmt.Sprintf("UPDATE %s SET %s %s LIMIT 1", r.TableName, setClause, whereClause)
		args := append(setArgs, whereArgs...)
		_, err := r.DB.ExecContext(ctx, query, args...)
		return err
	}

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
// On PostgreSQL, this is a single atomic statement via RETURNING.
// On MySQL/SQLite, the update+select happen inside a transaction to keep
// consistency under concurrent writers.
func (r *BaseRepository[T, ID]) UpdateAndFindOne(ctx context.Context, filter Filter, data Filter) (*T, error) {
	if r.Dialect.supportsReturning() {
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

	return WithTransactionResult(ctx, r.DB, func(tx *sql.Tx) (*T, error) {
		// Lock/select first to pin the row we are updating.
		selectSet, selectArgs := r.buildWhere(filter, 0)
		selectQuery := fmt.Sprintf(
			"SELECT %s FROM %s %s LIMIT 1",
			r.columnsString(),
			r.TableName,
			selectSet,
		)
		row := tx.QueryRowContext(ctx, selectQuery, selectArgs...)
		existing, err := r.scanRow(row)
		if err != nil || existing == nil {
			return existing, err
		}

		id, hasID := r.modelID(existing)
		setClause, setArgs := r.buildSetClause(data, 0)
		var (
			updateQuery string
			updateArgs  []any
		)
		if hasID {
			updateQuery = fmt.Sprintf(
				"UPDATE %s SET %s WHERE %s = %s",
				r.TableName,
				setClause,
				r.IDColumn,
				r.Dialect.placeholder(len(setArgs)+1),
			)
			updateArgs = append(append([]any{}, setArgs...), id)
		} else {
			whereClause, whereArgs := r.buildWhere(filter, len(setArgs))
			updateQuery = fmt.Sprintf("UPDATE %s SET %s %s", r.TableName, setClause, whereClause)
			updateArgs = append(append([]any{}, setArgs...), whereArgs...)
		}
		if _, err := tx.ExecContext(ctx, updateQuery, updateArgs...); err != nil {
			return nil, err
		}

		if hasID {
			idQuery := fmt.Sprintf(
				"SELECT %s FROM %s WHERE %s = %s LIMIT 1",
				r.columnsString(),
				r.TableName,
				r.IDColumn,
				r.Dialect.placeholder(1),
			)
			return r.scanRow(tx.QueryRowContext(ctx, idQuery, id))
		}

		row = tx.QueryRowContext(ctx, selectQuery, selectArgs...)
		return r.scanRow(row)
	})
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
		"UPDATE %s SET %s = %s + %s %s",
		r.TableName,
		column,
		column,
		r.Dialect.placeholder(1),
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
		"UPDATE %s SET %s = %s - %s %s",
		r.TableName,
		column,
		column,
		r.Dialect.placeholder(1),
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
	if len(updateParts) == 0 {
		if r.Dialect == DialectMySQL {
			updateParts = append(updateParts, fmt.Sprintf("%s = %s", r.IDColumn, r.IDColumn))
		} else {
			query := fmt.Sprintf(
				"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING",
				r.TableName,
				strings.Join(cols, ", "),
				r.Dialect.placeholders(1, len(cols)),
				strings.Join(conflictColumns, ", "),
			)
			if r.Dialect.supportsReturning() {
				row := r.DB.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
				return r.scanRow(row)
			}
			result, err := r.DB.ExecContext(ctx, query, values...)
			if err != nil {
				return nil, err
			}
			if id, err := result.LastInsertId(); err == nil {
				r.setGeneratedID(data, id)
			}
			return data, nil
		}
	}

	if r.Dialect == DialectMySQL {
		for i, col := range updateParts {
			if !strings.Contains(col, "EXCLUDED.") {
				continue
			}
			column := strings.SplitN(col, " = ", 2)[0]
			updateParts[i] = fmt.Sprintf("%s = VALUES(%s)", column, column)
		}
		query := fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
			r.TableName,
			strings.Join(cols, ", "),
			r.Dialect.placeholders(1, len(cols)),
			strings.Join(updateParts, ", "),
		)
		result, err := r.DB.ExecContext(ctx, query, values...)
		if err != nil {
			return nil, err
		}
		if id, err := result.LastInsertId(); err == nil {
			r.setGeneratedID(data, id)
		}
		return data, nil
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
		r.TableName,
		strings.Join(cols, ", "),
		r.Dialect.placeholders(1, len(cols)),
		strings.Join(conflictColumns, ", "),
		strings.Join(updateParts, ", "),
	)
	if r.Dialect.supportsReturning() {
		row := r.DB.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		return r.scanRow(row)
	}
	result, err := r.DB.ExecContext(ctx, query, values...)
	if err != nil {
		return nil, err
	}
	if id, err := result.LastInsertId(); err == nil {
		r.setGeneratedID(data, id)
	}
	return data, nil
}
