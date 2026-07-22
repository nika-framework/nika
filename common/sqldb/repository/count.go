package repository

import (
	"context"
	"fmt"
)

// CountByCondition counts records matching the given filter.
func (r *BaseRepository[T, ID]) CountByCondition(ctx context.Context, filter Filter) (int64, error) {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s %s",
		r.TableName,
		whereClause,
	)

	var count int64
	err := r.DB.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
