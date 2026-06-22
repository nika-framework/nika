package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (r *BaseRepository[T]) UpdateOneByID(
	ctx context.Context,
	id primitive.ObjectID,
	doc Filter,
) error {

	return r.UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{
			"$set": doc,
		},
	)
}

func (r *BaseRepository[T]) UpdateOne(
	ctx context.Context,
	filter Filter,
	update any,
) error {

	_, err := r.Collection.UpdateOne(
		ctx,
		filter,
		update,
	)

	return err
}

func (r *BaseRepository[T]) UpdateAndFindOne(
	ctx context.Context,
	filter Filter,
	update any,
) (*T, error) {

	err := r.UpdateOne(
		ctx,
		filter,
		update,
	)

	if err != nil {
		return nil, err
	}

	return r.FindOne(
		ctx,
		filter,
	)
}

func (r *BaseRepository[T]) UpdateMany(
	ctx context.Context,
	filter Filter,
	update any,
) error {

	_, err := r.Collection.UpdateMany(
		ctx,
		filter,
		update,
	)

	return err
}


func (r *BaseRepository[T]) Increment(
	ctx context.Context,
	filter Filter,
	key string,
	value int64,
) error {

	if value < 1 {
		return errors.New("db not increment zero")
	}

	_, err := r.Collection.UpdateOne(
		ctx,
		filter,
		bson.M{
			"$inc": bson.M{
				key: value,
			},
		},
	)

	return err
}

func (r *BaseRepository[T]) Decrement(
	ctx context.Context,
	filter Filter,
	key string,
	value int64,
) error {

	if value < 1 {
		return errors.New("db not decrement zero")
	}

	_, err := r.Collection.UpdateOne(
		ctx,
		filter,
		bson.M{
			"$inc": bson.M{
				key: -value,
			},
		},
	)

	return err
}