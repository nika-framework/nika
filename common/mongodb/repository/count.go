package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

// CountByCondition returns the exact number of documents matching filter.
// For very large collections where an exact count is not required, prefer
// r.Collection.EstimatedDocumentCount() directly.
func (r *BaseRepository[T]) CountByCondition(
	ctx context.Context,
	filter Filter,
) (int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	return r.Collection.CountDocuments(ctx, filter)
}
