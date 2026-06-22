package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)


func (r *BaseRepository[T]) Create(
	ctx context.Context,
	data *T,
) (*T, error) {
	res, err := r.Collection.InsertOne(ctx, data)
	if err != nil {
		return nil, err
	}
	// Merge the inserted ID into the provided data without a second DB round-trip.
	if res.InsertedID != nil {
		var m bson.M
		// Marshal the current data to BSON, unmarshal into a map, set _id, then unmarshal back.
		// This lets the BSON tags on the struct handle the ID field mapping without reflection.
		b, err := bson.Marshal(data)
		if err != nil {
			return nil, err
		}
		if err := bson.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		m["_id"] = res.InsertedID
		b2, err := bson.Marshal(m)
		if err != nil {
			return nil, err
		}
		if err := bson.Unmarshal(b2, data); err != nil {
			return nil, err
		}
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
