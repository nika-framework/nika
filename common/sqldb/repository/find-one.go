package repository

import (
	"context"
	"fmt"
)

// FindOneByID retrieves a single record by its primary key.
func (r *BaseRepository[T, ID]) FindOneByID(ctx context.Context, id ID) (*T, error) {
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = $1 LIMIT 1",
		r.columnsString(),
		r.TableName,
		r.IDColumn,
	)

	row := r.DB.QueryRowContext(ctx, query, id)
	return r.scanRow(row)
}

// FindOne retrieves the first record matching the given filter.
func (r *BaseRepository[T, ID]) FindOne(ctx context.Context, filter Filter) (*T, error) {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"SELECT %s FROM %s %s LIMIT 1",
		r.columnsString(),
		r.TableName,
		whereClause,
	)

	row := r.DB.QueryRowContext(ctx, query, args...)
	return r.scanRow(row)
}
