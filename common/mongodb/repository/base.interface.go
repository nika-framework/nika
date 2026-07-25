package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Filter is a MongoDB query document.
//
// TRUSTED INPUT. Every method that takes a Filter hands it to the driver
// unchanged, so anything in it is interpreted as query syntax — including
// operators. A filter built directly from a JSON request body is a NoSQL
// injection: a login check written as FindOne(ctx, Filter{"email": e,
// "password": p}) matches any account when the client sends
// {"password": {"$ne": null}}, and {"$where": "..."} executes JavaScript on the
// database server.
//
// Run anything derived from user input through SanitizeFilter (equality only) or
// SanitizeUserFilter (equality plus a small allowlist of selection operators)
// before it reaches these methods, or call the *Safe wrappers, which do it for
// you.
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

	// FindOne takes a trusted filter; see the Filter doc comment.
	FindOne(ctx context.Context, filter Filter, opts ...*options.FindOneOptions) (*T, error)

	// FindOneSafe sanitizes a user-supplied filter before querying.
	FindOneSafe(ctx context.Context, filter Filter, opts ...*options.FindOneOptions) (*T, error)

	ExistsByID(ctx context.Context, id primitive.ObjectID) (bool, error)
	ExistsByCondition(ctx context.Context, filter Filter) (bool, error)

	// FindByCondition takes a trusted filter. Without an options limit it reads
	// every match into memory.
	FindByCondition(ctx context.Context, filter Filter, opts ...*options.FindOptions) ([]T, error)

	// FindByConditionSafe sanitizes a user-supplied filter and applies
	// DefaultFindLimit unless the caller sets one.
	FindByConditionSafe(ctx context.Context, filter Filter, opts ...*options.FindOptions) ([]T, error)

	CountByCondition(ctx context.Context, filter Filter) (int64, error)

	Increment(ctx context.Context, filter Filter, key string, value int64) error

	Decrement(ctx context.Context, filter Filter, key string, value int64) error

	FindWithRelations(ctx context.Context, filter Filter, opts ...*options.FindOptions) ([]T, error)

	FindWithAggregate(ctx context.Context, pipeline []any) ([]map[string]any, error)

	// FindAll caps the result at DefaultFindLimit unless the caller overrides it.
	FindAll(ctx context.Context, filter Filter, opts ...*options.FindOptions) ([]T, error)

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
