package repository

import (
	"context"
	"fmt"
)

// FindByCondition retrieves all records matching the given filter.
func (r *BaseRepository[T, ID]) FindByCondition(ctx context.Context, filter Filter) ([]T, error) {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"SELECT %s FROM %s %s",
		r.columnsString(),
		r.TableName,
		whereClause,
	)

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

// RawQuery executes a raw SQL query and scans results into the model slice.
func (r *BaseRepository[T, ID]) RawQuery(ctx context.Context, query string, args ...any) ([]T, error) {
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("raw query error: %w", err)
	}

	return r.scanRows(rows)
}
