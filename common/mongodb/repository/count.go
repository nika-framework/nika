package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (r *BaseRepository[T]) ExistsByID(
	ctx context.Context,
	id primitive.ObjectID,
) (bool, error) {
	return r.ExistsByCondition(
		ctx,
		bson.M{"_id": id},
	)
}

func (r *BaseRepository[T]) ExistsByCondition(
	ctx context.Context,
	filter Filter,
) (bool, error) {

	count, err := r.Collection.CountDocuments(
		ctx,
		filter,
	)

	return count > 0, err
}