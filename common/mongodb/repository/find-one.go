package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func (r *BaseRepository[T]) FindOne(
	ctx context.Context,
	filter Filter,
) (*T, error) {
	var result T
	err := r.Collection.
		FindOne(ctx, filter).
		Decode(&result)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil

}
func (r *BaseRepository[T]) FindOneByID(
	ctx context.Context,
	id primitive.ObjectID,
) (*T, error) {
	var result T
	err := r.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}
