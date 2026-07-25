package repository

import (
	"context"
	"fmt"
)

func (r *BaseRepository[T, ID]) existsByIDQuery() string {
	return fmt.Sprintf(
		"SELECT EXISTS(SELECT 1 FROM %s WHERE %s = %s)",
		r.quotedTable,
		r.quotedID,
		r.Dialect.placeholder(1),
	)
}

// ExistsByID checks if a record with the given ID exists.
func (r *BaseRepository[T, ID]) ExistsByID(ctx context.Context, id ID) (bool, error) {
	var exists bool
	err := r.DB.QueryRowContext(ctx, r.existsByIDQuery(), id).Scan(&exists)
	return exists, err
}

func (r *BaseRepository[T, ID]) existsQuery(filter Filter) (string, []any, error) {
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}
	inner := joinSQL("SELECT 1 FROM "+r.quotedTable, whereClause)
	return "SELECT EXISTS(" + inner + ")", args, nil
}

// ExistsByCondition checks if any record matches the given filter.
func (r *BaseRepository[T, ID]) ExistsByCondition(ctx context.Context, filter Filter) (bool, error) {
	query, args, err := r.existsQuery(filter)
	if err != nil {
		return false, err
	}

	var exists bool
	err = r.DB.QueryRowContext(ctx, query, args...).Scan(&exists)
	return exists, err
}
