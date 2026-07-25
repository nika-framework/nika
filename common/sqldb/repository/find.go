package repository

import (
	"context"
	"fmt"
)

func (r *BaseRepository[T, ID]) findByConditionQuery(filter Filter) (string, []any, error) {
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}
	return joinSQL("SELECT "+r.columnsString(), "FROM "+r.quotedTable, whereClause), args, nil
}

// FindByCondition retrieves all records matching the given filter.
func (r *BaseRepository[T, ID]) FindByCondition(ctx context.Context, filter Filter) ([]T, error) {
	query, args, err := r.findByConditionQuery(filter)
	if err != nil {
		return nil, err
	}

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find by condition error: %w", err)
	}

	return r.scanRows(rows)
}

// FindAll retrieves all records, optionally filtered.
func (r *BaseRepository[T, ID]) FindAll(ctx context.Context, filter Filter) ([]T, error) {
	return r.FindByCondition(ctx, filter)
}

func (r *BaseRepository[T, ID]) findByWhereQuery(conds []Cond) (string, []any, error) {
	whereClause, args, _, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return "", nil, err
	}
	return joinSQL("SELECT "+r.columnsString(), "FROM "+r.quotedTable, whereClause), args, nil
}

// FindByWhere retrieves all records matching the typed conditions, which support
// the full operator set (ranges, IN, LIKE, NULL checks) without ever letting an
// operator or column arrive as raw SQL text.
func (r *BaseRepository[T, ID]) FindByWhere(ctx context.Context, conds []Cond) ([]T, error) {
	query, args, err := r.findByWhereQuery(conds)
	if err != nil {
		return nil, err
	}

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find by where error: %w", err)
	}

	return r.scanRows(rows)
}

// RawQuery executes a raw SQL query and scans the results into the model slice.
//
// CONTRACT: query is sent to the database verbatim. Nothing in it is validated,
// quoted, or escaped. Every value must be passed through args as a bound
// parameter — never build the statement with fmt.Sprintf from request data, and
// never interpolate a table, column, or ORDER BY name that a client can
// influence. Use FindByWhere for anything driven by user input; this method
// exists for hand-written SQL that the developer owns end to end.
func (r *BaseRepository[T, ID]) RawQuery(ctx context.Context, query string, args ...any) ([]T, error) {
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("raw query error: %w", err)
	}

	return r.scanRows(rows)
}
