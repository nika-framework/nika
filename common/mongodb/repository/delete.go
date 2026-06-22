package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (r *BaseRepository[T]) DeleteByID(
	ctx context.Context,
	id primitive.ObjectID,
) error { 
	_, err := r.Collection.DeleteOne(
		ctx,
		bson.M{"_id": id},
	)

	return err
}



func (r *BaseRepository[T]) DeleteMany(
	ctx context.Context,
	filter Filter,
) error {

	_, err := r.Collection.DeleteMany(
		ctx,
		filter,
	)

	return err
}

func (r *BaseRepository[T]) DeleteOne(
	ctx context.Context,
	filter Filter,
) error {

	_, err := r.Collection.DeleteOne(
		ctx,
		filter,
	)

	return err
}