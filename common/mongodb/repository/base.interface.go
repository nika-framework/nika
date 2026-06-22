package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Filter = map[string]any
type Pipeline = mongo.Pipeline
type PaginationResult struct {
	Data  []map[string]any `json:"data"`
	Total int64            `json:"total"`
}

type IBaseRepository[T any] interface {
	Create(ctx context.Context, data *T) (*T, error)
	CreateAndUpdate(ctx context.Context, data *T) error

	SaveOne(ctx context.Context, data *T) error
	InsertMany(ctx context.Context, data []T) error

	FindOneByID(ctx context.Context, id primitive.ObjectID) (*T, error)
	FindOne(ctx context.Context, filter Filter) (*T, error)

	ExistsByID(ctx context.Context, id primitive.ObjectID) (bool, error)
	ExistsByCondition(ctx context.Context, filter Filter) (bool, error)

	FindByCondition(ctx context.Context, filter Filter) ([]T, error)

	CountByCondition(ctx context.Context, filter Filter) (int64, error)

	Increment(ctx context.Context, filter Filter, key string, value int64) error

	Decrement(ctx context.Context, filter Filter, key string, value int64) error

	FindWithRelations(ctx context.Context, filter Filter) ([]T, error)

	FindWithAggregate(ctx context.Context, pipeline []any) ([]map[string]any, error)

	FindAll(ctx context.Context, filter Filter) ([]T, error)

	UpdateOneByID(ctx context.Context, id primitive.ObjectID, doc Filter) error

	UpdateOne(ctx context.Context, filter Filter, update any) error

	UpdateAndFindOne(ctx context.Context, filter Filter, update any) (*T, error)

	UpdateMany(ctx context.Context, filter Filter, update any) error

	DeleteByID(ctx context.Context, id primitive.ObjectID) error

	DeleteMany(ctx context.Context, filter Filter) error

	DeleteOne(ctx context.Context, filter Filter) error

	Pages(
		ctx context.Context,
		pipeline []any,
		page int64,
		count int64,
	) (*PaginationResult, error)
}
