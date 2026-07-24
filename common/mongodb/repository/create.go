package repository

import (
	"context"
	"fmt"
)

// insertBatchLimit caps rows per InsertMany batch so a single failed batch
// does not force retrying millions of rows.
const insertBatchLimit = 500

func (r *BaseRepository[T]) Create(
	ctx context.Context,
	data *T,
) (*T, error) {
	if data == nil {
		return nil, fmt.Errorf("create: data is nil")
	}
	res, err := r.Collection.InsertOne(ctx, data)
	if err != nil {
		return nil, err
	}
	if res.InsertedID != nil {
		setInsertedID(data, res.InsertedID)
	}
	return data, nil
}

func (r *BaseRepository[T]) CreateAndUpdate(
	ctx context.Context,
	data *T,
) error {
	if data == nil {
		return fmt.Errorf("create: data is nil")
	}
	_, err := r.Collection.InsertOne(ctx, data)
	return err
}

func (r *BaseRepository[T]) SaveOne(
	ctx context.Context,
	data *T,
) error {
	if data == nil {
		return fmt.Errorf("save: data is nil")
	}
	_, err := r.Collection.InsertOne(ctx, data)
	return err
}

// InsertMany chunks the input into safe batch sizes and inserts each chunk.
// It fails fast on the first failed chunk.
func (r *BaseRepository[T]) InsertMany(
	ctx context.Context,
	data []T,
) error {
	if len(data) == 0 {
		return nil
	}

	for start := 0; start < len(data); start += insertBatchLimit {
		end := start + insertBatchLimit
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]

		docs := make([]interface{}, len(chunk))
		for i := range chunk {
			docs[i] = chunk[i]
		}

		if _, err := r.Collection.InsertMany(ctx, docs); err != nil {
			return err
		}
	}
	return nil
}
