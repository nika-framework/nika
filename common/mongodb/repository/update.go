package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (r *BaseRepository[T]) UpdateOneByID(
	ctx context.Context,
	id primitive.ObjectID,
	doc Filter,
) error {
	return r.UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": doc},
	)
}

func (r *BaseRepository[T]) UpdateOne(
	ctx context.Context,
	filter Filter,
	update any,
) error {
	_, err := r.Collection.UpdateOne(ctx, filter, update)
	return err
}

// UpdateAndFindOne atomically updates a document and returns the version
// after the update via findAndModify — no race window between write and read.
func (r *BaseRepository[T]) UpdateAndFindOne(
	ctx context.Context,
	filter Filter,
	update any,
) (*T, error) {
	opts := options.FindOneAndUpdate().
		SetReturnDocument(options.After).
		SetUpsert(false)

	var result T
	err := r.Collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

func (r *BaseRepository[T]) UpdateMany(
	ctx context.Context,
	filter Filter,
	update any,
) error {
	_, err := r.Collection.UpdateMany(ctx, filter, update)
	return err
}

func (r *BaseRepository[T]) Increment(
	ctx context.Context,
	filter Filter,
	key string,
	value int64,
) error {
	if value < 1 {
		return errors.New("increment value must be positive")
	}
	_, err := r.Collection.UpdateOne(
		ctx,
		filter,
		bson.M{"$inc": bson.M{key: value}},
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
		return errors.New("decrement value must be positive")
	}
	_, err := r.Collection.UpdateOne(
		ctx,
		filter,
		bson.M{"$inc": bson.M{key: -value}},
	)
	return err
}
