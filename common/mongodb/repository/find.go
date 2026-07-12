package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

func (r *BaseRepository[T]) FindWithRelations(
	ctx context.Context,
	filter Filter,
) ([]T, error) {
	return r.FindByCondition(ctx, filter)
}

func (r *BaseRepository[T]) FindAll(
	ctx context.Context,
	filter Filter,
) ([]T, error) {

	if len(filter) == 0 {
		filter = bson.M{}
	}

	return r.FindByCondition(
		ctx,
		filter,
	)
}

func (r *BaseRepository[T]) FindWithAggregate(
	ctx context.Context,
	pipeline []any,
) ([]map[string]any, error) {

	cursor, err := r.Collection.Aggregate(
		ctx,
		pipeline,
	)

	if err != nil {
		return nil, err
	}

	defer cursor.Close(ctx)

	var result []map[string]any

	if err := cursor.All(ctx, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *BaseRepository[T]) FindByCondition(
	ctx context.Context,
	filter Filter,
) ([]T, error) {

	cursor, err := r.Collection.Find(
		ctx,
		filter,
	)

	if err != nil {
		return nil, err
	}

	defer cursor.Close(ctx)

	var result []T

	err = cursor.All(ctx, &result)

	return result, err
}
