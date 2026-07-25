package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FindOne returns the first document matching filter, or (nil, nil) when there
// is none.
//
// filter is trusted input and is handed to the driver as-is: see the Filter doc
// comment. Use FindOneSafe when any part of it comes from a request.
func (r *BaseRepository[T]) FindOne(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOneOptions,
) (*T, error) {
	var result T
	err := r.Collection.
		FindOne(ctx, filter, opts...).
		Decode(&result)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil

}

// FindOneSafe is FindOne for a filter built from user input: the filter goes
// through SanitizeUserFilter first, so a JSON body of {"password": {"$ne": null}}
// is rejected rather than turned into "any user with a password".
func (r *BaseRepository[T]) FindOneSafe(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOneOptions,
) (*T, error) {
	clean, err := SanitizeUserFilter(filter)
	if err != nil {
		return nil, err
	}
	return r.FindOne(ctx, clean, opts...)
}
func (r *BaseRepository[T]) FindOneByID(
	ctx context.Context,
	id primitive.ObjectID,
) (*T, error) {
	var result T
	err := r.Collection.FindOne(ctx, bson.M{"_id": id}).Decode(&result)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}

		return nil, err
	}

	return &result, nil
}
