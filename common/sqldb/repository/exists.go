package repository

import (
	"context"
	"fmt"
)

// ExistsByID checks if a record with the given ID exists.
func (r *BaseRepository[T, ID]) ExistsByID(ctx context.Context, id ID) (bool, error) {
	query := fmt.Sprintf(
		"SELECT EXISTS(SELECT 1 FROM %s WHERE %s = $1)",
		r.TableName,
		r.IDColumn,
	)

	var exists bool
	err := r.DB.QueryRowContext(ctx, query, id).Scan(&exists)
	return exists, err
}

// ExistsByCondition checks if any record matches the given filter.
func (r *BaseRepository[T, ID]) ExistsByCondition(ctx context.Context, filter Filter) (bool, error) {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"SELECT EXISTS(SELECT 1 FROM %s %s)",
		r.TableName,
		whereClause,
	)

	var exists bool
	err := r.DB.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists, err
}
