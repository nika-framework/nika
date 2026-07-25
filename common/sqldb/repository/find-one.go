package repository

import (
	"context"
	"fmt"
)

// findOneByIDQuery is separated from FindOneByID so the generated SQL can be
// asserted without a live database. The same split exists for every statement
// builder in this package.
func (r *BaseRepository[T, ID]) findOneByIDQuery() string {
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = %s LIMIT 1",
		r.columnsString(),
		r.quotedTable,
		r.quotedID,
		r.Dialect.placeholder(1),
	)
}

// FindOneByID retrieves a single record by its primary key.
// A missing row is reported as (nil, nil), not as an error.
func (r *BaseRepository[T, ID]) FindOneByID(ctx context.Context, id ID) (*T, error) {
	row := r.DB.QueryRowContext(ctx, r.findOneByIDQuery(), id)
	return r.scanRow(row)
}

func (r *BaseRepository[T, ID]) findOneQuery(filter Filter) (string, []any, error) {
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}

	return joinSQL(
		"SELECT "+r.columnsString(),
		"FROM "+r.quotedTable,
		whereClause,
		"LIMIT 1",
	), args, nil
}

// FindOne retrieves the first record matching the given filter.
// A missing row is reported as (nil, nil), not as an error.
func (r *BaseRepository[T, ID]) FindOne(ctx context.Context, filter Filter) (*T, error) {
	query, args, err := r.findOneQuery(filter)
	if err != nil {
		return nil, err
	}

	row := r.DB.QueryRowContext(ctx, query, args...)
	return r.scanRow(row)
}

// FindOneByWhere retrieves the first record matching the typed conditions.
func (r *BaseRepository[T, ID]) FindOneByWhere(ctx context.Context, conds []Cond) (*T, error) {
	whereClause, args, _, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return nil, err
	}

	query := joinSQL("SELECT "+r.columnsString(), "FROM "+r.quotedTable, whereClause, "LIMIT 1")
	return r.scanRow(r.DB.QueryRowContext(ctx, query, args...))
}
