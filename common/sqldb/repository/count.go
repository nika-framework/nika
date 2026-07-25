package repository

import "context"

func (r *BaseRepository[T, ID]) countQuery(filter Filter) (string, []any, error) {
	whereClause, args, err := r.buildWhere(filter, 0)
	if err != nil {
		return "", nil, err
	}
	return joinSQL("SELECT COUNT(*) FROM "+r.quotedTable, whereClause), args, nil
}

// CountByCondition counts records matching the given filter.
func (r *BaseRepository[T, ID]) CountByCondition(ctx context.Context, filter Filter) (int64, error) {
	query, args, err := r.countQuery(filter)
	if err != nil {
		return 0, err
	}

	var count int64
	err = r.DB.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

func (r *BaseRepository[T, ID]) countByWhereQuery(conds []Cond) (string, []any, error) {
	whereClause, args, _, err := buildWhereConds(r.Dialect, conds, 0)
	if err != nil {
		return "", nil, err
	}
	return joinSQL("SELECT COUNT(*) FROM "+r.quotedTable, whereClause), args, nil
}

// CountByWhere counts records matching the typed conditions.
func (r *BaseRepository[T, ID]) CountByWhere(ctx context.Context, conds []Cond) (int64, error) {
	query, args, err := r.countByWhereQuery(conds)
	if err != nil {
		return 0, err
	}

	var count int64
	err = r.DB.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
