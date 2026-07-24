package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ExistsByID reports whether a document with the given _id exists.
func (r *BaseRepository[T]) ExistsByID(
	ctx context.Context,
	id primitive.ObjectID,
) (bool, error) {
	return r.ExistsByCondition(ctx, bson.M{"_id": id})
}

// ExistsByCondition reports whether at least one document matches the filter.
// Uses a bounded FindOne so it terminates as soon as the first document is
// located — CountDocuments would scan far more than necessary at scale.
func (r *BaseRepository[T]) ExistsByCondition(
	ctx context.Context,
	filter Filter,
) (bool, error) {
	if filter == nil {
		filter = bson.M{}
	}
	opts := options.FindOne().SetProjection(bson.M{"_id": 1})
	err := r.Collection.FindOne(ctx, filter, opts).Err()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	return false, err
}
