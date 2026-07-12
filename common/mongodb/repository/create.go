package repository

import (
	"context"
)

func (r *BaseRepository[T]) Create(
	ctx context.Context,
	data *T,
) (*T, error) {
	res, err := r.Collection.InsertOne(ctx, data)
	if err != nil {
		return nil, err
	}
	// Try to inject the inserted _id into the struct's BSON "_id" field if present.
	// This avoids the previous 4x marshal/unmarshal round-trip on every insert.
	if res.InsertedID != nil {
		setInsertedID(data, res.InsertedID)
	}
	return data, nil
}

func (r *BaseRepository[T]) CreateAndUpdate(
	ctx context.Context,
	data *T,
) error {
	_, err := r.Collection.InsertOne(ctx, data)
	return err
}

func (r *BaseRepository[T]) SaveOne(
	ctx context.Context,
	data *T,
) error {
	_, err := r.Collection.InsertOne(ctx, data)
	return err
}

func (r *BaseRepository[T]) InsertMany(
	ctx context.Context,
	data []T,
) error {

	docs := make([]interface{}, len(data))

	for i := range data {
		docs[i] = data[i]
	}

	_, err := r.Collection.InsertMany(ctx, docs)

	return err
}
