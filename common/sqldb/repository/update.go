package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (r *BaseRepository[T, ID]) updateByIDQuery(data Filter) (string, []any, error) {
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return "", nil, err
	}

	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s = %s",
		r.quotedTable,
		setClause,
		r.quotedID,
		// The ID placeholder continues the numbering the SET clause started.
		r.Dialect.placeholder(len(setArgs)+1),
	)
	return query, setArgs, nil
}

// UpdateOneByID updates a single record by its primary key.
func (r *BaseRepository[T, ID]) UpdateOneByID(ctx context.Context, id ID, data Filter) error {
	query, setArgs, err := r.updateByIDQuery(data)
	if err != nil {
		return err
	}

	_, err = r.DB.ExecContext(ctx, query, concatArgs(setArgs, []any{id})...)
	return err
}

func (r *BaseRepository[T, ID]) updateOneQuery(filter Filter, data Filter) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return "", nil, err
	}
	// The predicate's placeholders must continue where the SET clause stopped;
	// starting them at 1 again would bind the wrong values on PostgreSQL.
	whereClause, whereArgs, err := r.buildWhere(filter, len(setArgs))
	if err != nil {
		return "", nil, err
	}

	args := concatArgs(setArgs, whereArgs)

	if r.Dialect == DialectMySQL {
		// MySQL cannot reference the update target in a subquery; LIMIT on
		// UPDATE is its way to touch a single row.
		return joinSQL("UPDATE "+r.quotedTable, "SET "+setClause, whereClause, "LIMIT 1"), args, nil
	}

	// PostgreSQL and SQLite both accept LIMIT inside IN (SELECT ...).
	inner := joinSQL("SELECT "+r.quotedID, "FROM "+r.quotedTable, whereClause, "LIMIT 1")
	query := fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s IN (%s)",
		r.quotedTable,
		setClause,
		r.quotedID,
		inner,
	)
	return query, args, nil
}

// UpdateOne updates the first record matching the filter.
// An empty filter is rejected: it would update an arbitrary row.
func (r *BaseRepository[T, ID]) UpdateOne(ctx context.Context, filter Filter, data Filter) error {
	query, args, err := r.updateOneQuery(filter, data)
	if err != nil {
		return err
	}

	_, err = r.DB.ExecContext(ctx, query, args...)
	return err
}

func (r *BaseRepository[T, ID]) updateReturningQuery(filter Filter, data Filter) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return "", nil, err
	}
	whereClause, whereArgs, err := r.buildWhere(filter, len(setArgs))
	if err != nil {
		return "", nil, err
	}

	query := joinSQL(
		"UPDATE "+r.quotedTable,
		"SET "+setClause,
		whereClause,
		"RETURNING "+r.columnsString(),
	)
	return query, concatArgs(setArgs, whereArgs), nil
}

// UpdateAndFindOne updates a record and returns the updated version.
// On PostgreSQL, this is a single atomic statement via RETURNING.
// On MySQL/SQLite, the update+select happen inside a transaction to keep
// consistency under concurrent writers.
//
// A filter that matches nothing yields (nil, nil).
func (r *BaseRepository[T, ID]) UpdateAndFindOne(ctx context.Context, filter Filter, data Filter) (*T, error) {
	if len(filter) == 0 {
		return nil, ErrEmptyFilter
	}

	if r.Dialect.supportsReturning() {
		query, args, err := r.updateReturningQuery(filter, data)
		if err != nil {
			return nil, err
		}
		return r.scanRow(r.DB.QueryRowContext(ctx, query, args...))
	}

	return WithTransactionResult(ctx, r.DB, func(tx *sql.Tx) (*T, error) {
		// Select first to pin the row we are updating.
		selectWhere, selectArgs, err := r.buildWhere(filter, 0)
		if err != nil {
			return nil, err
		}
		selectQuery := joinSQL(
			"SELECT "+r.columnsString(),
			"FROM "+r.quotedTable,
			selectWhere,
			"LIMIT 1",
		)
		row := tx.QueryRowContext(ctx, selectQuery, selectArgs...)
		existing, err := r.scanRow(row)
		if err != nil || existing == nil {
			return existing, err
		}

		id, hasID := r.modelID(existing)
		setClause, setArgs, err := r.buildSetClause(data, 0)
		if err != nil {
			return nil, err
		}

		var (
			updateQuery string
			updateArgs  []any
		)
		if hasID {
			updateQuery = fmt.Sprintf(
				"UPDATE %s SET %s WHERE %s = %s",
				r.quotedTable,
				setClause,
				r.quotedID,
				r.Dialect.placeholder(len(setArgs)+1),
			)
			updateArgs = concatArgs(setArgs, []any{id})
		} else {
			whereClause, whereArgs, err := r.buildWhere(filter, len(setArgs))
			if err != nil {
				return nil, err
			}
			updateQuery = joinSQL("UPDATE "+r.quotedTable, "SET "+setClause, whereClause)
			updateArgs = concatArgs(setArgs, whereArgs)
		}
		if _, err := tx.ExecContext(ctx, updateQuery, updateArgs...); err != nil {
			return nil, err
		}

		if hasID {
			idQuery := fmt.Sprintf(
				"SELECT %s FROM %s WHERE %s = %s LIMIT 1",
				r.columnsString(),
				r.quotedTable,
				r.quotedID,
				r.Dialect.placeholder(1),
			)
			return r.scanRow(tx.QueryRowContext(ctx, idQuery, id))
		}

		return r.scanRow(tx.QueryRowContext(ctx, selectQuery, selectArgs...))
	})
}

func (r *BaseRepository[T, ID]) updateManyQuery(filter Filter, data Filter) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return "", nil, err
	}
	whereClause, whereArgs, err := r.buildWhere(filter, len(setArgs))
	if err != nil {
		return "", nil, err
	}

	return joinSQL("UPDATE "+r.quotedTable, "SET "+setClause, whereClause), concatArgs(setArgs, whereArgs), nil
}

// UpdateMany updates all records matching the filter. Returns the number of affected rows.
//
// An empty filter is rejected with ErrEmptyFilter: `UpdateMany(ctx, Filter{},
// data)` used to emit `UPDATE t SET ...` with no predicate and rewrite every row.
// Use UpdateAllUnsafe when that is genuinely intended.
func (r *BaseRepository[T, ID]) UpdateMany(ctx context.Context, filter Filter, data Filter) (int64, error) {
	query, args, err := r.updateManyQuery(filter, data)
	if err != nil {
		return 0, err
	}

	result, err := r.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// UpdateByWhere updates every record matching the typed conditions.
func (r *BaseRepository[T, ID]) UpdateByWhere(ctx context.Context, conds []Cond, data Filter) (int64, error) {
	if len(conds) == 0 {
		return 0, ErrEmptyFilter
	}
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return 0, err
	}
	whereClause, whereArgs, _, err := buildWhereConds(r.Dialect, conds, len(setArgs))
	if err != nil {
		return 0, err
	}
	if whereClause == "" {
		return 0, ErrEmptyFilter
	}

	query := joinSQL("UPDATE "+r.quotedTable, "SET "+setClause, whereClause)
	result, err := r.DB.ExecContext(ctx, query, concatArgs(setArgs, whereArgs)...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// UpdateAllUnsafe updates every row in the table. The name is the warning: this
// is the intentional form of the accident UpdateMany now refuses.
func (r *BaseRepository[T, ID]) UpdateAllUnsafe(ctx context.Context, data Filter) (int64, error) {
	setClause, setArgs, err := r.buildSetClause(data, 0)
	if err != nil {
		return 0, err
	}

	result, err := r.DB.ExecContext(ctx, "UPDATE "+r.quotedTable+" SET "+setClause, setArgs...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// stepQuery renders an arithmetic UPDATE (`col = col ± ?`). op must be "+" or
// "-" and is supplied only by Increment/Decrement, never by a caller.
func (r *BaseRepository[T, ID]) stepQuery(filter Filter, column, op string) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	quotedCol, err := r.quote(column)
	if err != nil {
		return "", nil, fmt.Errorf("increment: %w", err)
	}
	// The step value takes placeholder 1; the predicate continues from there.
	whereClause, whereArgs, err := r.buildWhere(filter, 1)
	if err != nil {
		return "", nil, err
	}

	query := joinSQL(
		"UPDATE "+r.quotedTable,
		fmt.Sprintf("SET %s = %s %s %s", quotedCol, quotedCol, op, r.Dialect.placeholder(1)),
		whereClause,
	)
	return query, whereArgs, nil
}

// Increment increases a numeric column by the given value.
func (r *BaseRepository[T, ID]) Increment(ctx context.Context, filter Filter, column string, value int64) error {
	if value < 1 {
		return errors.New("increment value must be positive")
	}

	query, whereArgs, err := r.stepQuery(filter, column, "+")
	if err != nil {
		return err
	}

	_, err = r.DB.ExecContext(ctx, query, concatArgs([]any{value}, whereArgs)...)
	return err
}

// Decrement decreases a numeric column by the given value.
func (r *BaseRepository[T, ID]) Decrement(ctx context.Context, filter Filter, column string, value int64) error {
	if value < 1 {
		return errors.New("decrement value must be positive")
	}

	query, whereArgs, err := r.stepQuery(filter, column, "-")
	if err != nil {
		return err
	}

	_, err = r.DB.ExecContext(ctx, query, concatArgs([]any{value}, whereArgs)...)
	return err
}

// RawExec executes a raw SQL statement (INSERT, UPDATE, DELETE).
//
// CONTRACT: query is sent to the database verbatim. Nothing in it is validated,
// quoted, or escaped. Every value must travel through args as a bound parameter,
// and no identifier in it may come from request data. Use UpdateByWhere /
// DeleteByWhere for anything user-driven; this method is for SQL the developer
// writes and owns.
func (r *BaseRepository[T, ID]) RawExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.DB.ExecContext(ctx, query, args...)
}

// upsertQuery renders the dialect's INSERT ... ON CONFLICT form. It reports
// whether the statement can return a row, which is false for DO NOTHING when the
// conflict fires.
func (r *BaseRepository[T, ID]) upsertQuery(conflictColumns []string) (query string, alwaysReturnsRow bool, err error) {
	if len(r.insertCols) == 0 {
		return "", false, fmt.Errorf("upsert: no insertable columns")
	}

	quotedConflict := make([]string, 0, len(conflictColumns))
	conflictSet := make(map[string]struct{}, len(conflictColumns))
	for _, cc := range conflictColumns {
		quoted, qErr := r.quote(cc)
		if qErr != nil {
			return "", false, fmt.Errorf("upsert conflict column: %w", qErr)
		}
		quotedConflict = append(quotedConflict, quoted)
		conflictSet[cc] = struct{}{}
	}
	if len(quotedConflict) == 0 && r.Dialect != DialectMySQL {
		return "", false, fmt.Errorf("upsert: at least one conflict column is required")
	}

	// Every column except the conflict key gets overwritten on conflict.
	updateParts := make([]string, 0, len(r.insertCols))
	for i, col := range r.insertCols {
		if _, isConflict := conflictSet[col]; isConflict {
			continue
		}
		quoted := r.quotedInsert[i]
		if r.Dialect == DialectMySQL {
			updateParts = append(updateParts, fmt.Sprintf("%s = VALUES(%s)", quoted, quoted))
			continue
		}
		updateParts = append(updateParts, fmt.Sprintf("%s = EXCLUDED.%s", quoted, quoted))
	}

	insertHead := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		r.quotedTable,
		strings.Join(r.quotedInsert, ", "),
		r.Dialect.placeholders(1, len(r.insertCols)),
	)

	if r.Dialect == DialectMySQL {
		if len(updateParts) == 0 {
			// Assigning the key to itself is MySQL's idiomatic no-op update.
			updateParts = append(updateParts, fmt.Sprintf("%s = %s", r.quotedID, r.quotedID))
		}
		return insertHead + " ON DUPLICATE KEY UPDATE " + strings.Join(updateParts, ", "), true, nil
	}

	if len(updateParts) == 0 {
		return fmt.Sprintf("%s ON CONFLICT (%s) DO NOTHING", insertHead, strings.Join(quotedConflict, ", ")), false, nil
	}

	return fmt.Sprintf(
		"%s ON CONFLICT (%s) DO UPDATE SET %s",
		insertHead,
		strings.Join(quotedConflict, ", "),
		strings.Join(updateParts, ", "),
	), true, nil
}

// Upsert performs an INSERT ... ON CONFLICT ... DO UPDATE (or MySQL's
// ON DUPLICATE KEY UPDATE).
// conflictColumns: columns that form the unique constraint.
// data: the record to upsert.
func (r *BaseRepository[T, ID]) Upsert(ctx context.Context, data *T, conflictColumns ...string) (*T, error) {
	if data == nil {
		return nil, fmt.Errorf("upsert: data is nil")
	}

	query, alwaysReturnsRow, err := r.upsertQuery(conflictColumns)
	if err != nil {
		return nil, err
	}
	values := r.getValues(data, r.insertIdx)

	if r.Dialect.supportsReturning() {
		row := r.DB.QueryRowContext(ctx, query+" RETURNING "+r.columnsString(), values...)
		if alwaysReturnsRow {
			return r.scanRowRequired(row)
		}
		// DO NOTHING legitimately returns nothing when the conflict fires.
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
