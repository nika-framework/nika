package repository

import (
	"context"
	"fmt"
)

func (r *BaseRepository[T, ID]) deleteByIDQuery() string {
	return fmt.Sprintf(
		"DELETE FROM %s WHERE %s = %s",
		r.quotedTable,
		r.quotedID,
		r.Dialect.placeholder(1),
	)
}

// DeleteByID deletes a record by its primary key.
func (r *BaseRepository[T, ID]) DeleteByID(ctx context.Context, id ID) error {
	_, err := r.DB.ExecContext(ctx, r.deleteByIDQuery(), id)
	return err
}

func (r *BaseRepository[T, ID]) deleteOneQuery(filter Filter) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	// No SET clause precedes the predicate here, so placeholders start at 1
	// (startIdx 0) for both the direct and the subquery form.
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}

	if r.Dialect == DialectMySQL {
		// MySQL forbids referencing the delete target inside a subquery, so it
		// gets the LIMIT-on-DELETE form instead.
		return joinSQL("DELETE FROM "+r.quotedTable, whereClause, "LIMIT 1"), args, nil
	}

	// PostgreSQL and SQLite both accept LIMIT inside IN (SELECT ...), which is
	// how a single row is pinned without a dialect-specific rowid trick.
	inner := joinSQL("SELECT "+r.quotedID, "FROM "+r.quotedTable, whereClause, "LIMIT 1")
	return fmt.Sprintf(
		"DELETE FROM %s WHERE %s IN (%s)",
		r.quotedTable,
		r.quotedID,
		inner,
	), args, nil
}

// DeleteOne deletes the first record matching the filter.
// An empty filter is rejected: it would delete an arbitrary row.
func (r *BaseRepository[T, ID]) DeleteOne(ctx context.Context, filter Filter) error {
	query, args, err := r.deleteOneQuery(filter)
	if err != nil {
		return err
	}

	_, err = r.DB.ExecContext(ctx, query, args...)
	return err
}

func (r *BaseRepository[T, ID]) deleteManyQuery(filter Filter) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, ErrEmptyFilter
	}
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}
	return joinSQL("DELETE FROM "+r.quotedTable, whereClause), args, nil
}

// DeleteMany deletes all records matching the filter. Returns the number of deleted rows.
//
// An empty filter is rejected with ErrEmptyFilter: `DeleteMany(ctx, Filter{})`
// used to emit `DELETE FROM t` and empty the table, which is a plausible outcome
// of a handler whose filter map came out empty. Use DeleteAllUnsafe to truncate
// on purpose.
func (r *BaseRepository[T, ID]) DeleteMany(ctx context.Context, filter Filter) (int64, error) {
	query, args, err := r.deleteManyQuery(filter)
	if err != nil {
		return 0, err
	}

	result, err := r.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// DeleteByWhere deletes every record matching the typed conditions. An empty
// condition list is rejected for the same reason as DeleteMany.
func (r *BaseRepository[T, ID]) DeleteByWhere(ctx context.Context, conds []Cond) (int64, error) {
	if len(conds) == 0 {
		return 0, ErrEmptyFilter
	}
	whereClause, args, _, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return 0, err
	}
	if whereClause == "" {
		return 0, ErrEmptyFilter
	}

	result, err := r.DB.ExecContext(ctx, joinSQL("DELETE FROM "+r.quotedTable, whereClause), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteAllUnsafe deletes every row in the table. The name is the warning: this
// is the intentional form of the accident DeleteMany now refuses.
func (r *BaseRepository[T, ID]) DeleteAllUnsafe(ctx context.Context) (int64, error) {
	result, err := r.DB.ExecContext(ctx, "DELETE FROM "+r.quotedTable)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
