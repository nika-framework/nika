package repository

import (
	"context"
	"fmt"
)

// DeleteByID deletes a record by its primary key.
func (r *BaseRepository[T, ID]) DeleteByID(ctx context.Context, id ID) error {
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE %s = $1",
		r.TableName,
		r.IDColumn,
	)

	_, err := r.DB.ExecContext(ctx, query, id)
	return err
}

// DeleteOne deletes the first record matching the filter.
func (r *BaseRepository[T, ID]) DeleteOne(ctx context.Context, filter Filter) error {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"DELETE FROM %s WHERE %s IN (SELECT %s FROM %s %s LIMIT 1)",
		r.TableName,
		r.IDColumn,
		r.IDColumn,
		r.TableName,
		whereClause,
	)

	_, err := r.DB.ExecContext(ctx, query, args...)
	return err
}

// DeleteMany deletes all records matching the filter. Returns the number of deleted rows.
func (r *BaseRepository[T, ID]) DeleteMany(ctx context.Context, filter Filter) (int64, error) {
	whereClause, args := r.buildWhere(filter, 0)

	query := fmt.Sprintf(
		"DELETE FROM %s %s",
		r.TableName,
		whereClause,
	)

	result, err := r.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}
