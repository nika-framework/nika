package repository

import (
	"context" 
)


func (r *BaseRepository[T]) CountByCondition(
	ctx context.Context,
	filter Filter,
) (int64, error) {

	return r.Collection.CountDocuments(
		ctx,
		filter,
	)
}

