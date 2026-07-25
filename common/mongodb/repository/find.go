package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DefaultFindLimit bounds an otherwise unlimited read.
//
// FindAll(ctx, Filter{}) used to stream a whole collection through the cursor
// into one Go slice: memory grows with the collection, so a table that was fine
// in staging takes the process down in production. Callers that genuinely need
// more must ask for it explicitly with options.Find().SetLimit(...), which makes
// the cost visible at the call site.
const DefaultFindLimit int64 = 1000

func (r *BaseRepository[T]) FindWithRelations(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOptions,
) ([]T, error) {
	return r.FindByCondition(ctx, filter, opts...)
}

// FindAll retrieves records matching filter, capped at DefaultFindLimit unless
// the caller supplies its own limit.
func (r *BaseRepository[T]) FindAll(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOptions,
) ([]T, error) {
	if len(filter) == 0 {
		filter = bson.M{}
	}

	if !hasLimit(opts) {
		opts = append(opts, options.Find().SetLimit(DefaultFindLimit))
	}

	return r.FindByCondition(ctx, filter, opts...)
}

// hasLimit reports whether any of the supplied option sets pins a limit.
func hasLimit(opts []*options.FindOptions) bool {
	for _, o := range opts {
		if o != nil && o.Limit != nil {
			return true
		}
	}
	return false
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

// FindByCondition retrieves all records matching the given filter.
//
// filter is trusted input: see the Filter doc comment. Pass options.Find() to set
// a limit, sort, or projection — without a limit this reads every matching
// document into memory.
func (r *BaseRepository[T]) FindByCondition(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOptions,
) ([]T, error) {

	cursor, err := r.Collection.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}

	defer cursor.Close(ctx)

	var result []T

	err = cursor.All(ctx, &result)

	return result, err
}

// FindByConditionSafe is FindByCondition for a filter built from user input: the
// filter is passed through SanitizeUserFilter first, so a request body cannot
// inject query operators, and a limit is applied when the caller does not set one.
func (r *BaseRepository[T]) FindByConditionSafe(
	ctx context.Context,
	filter Filter,
	opts ...*options.FindOptions,
) ([]T, error) {
	clean, err := SanitizeUserFilter(filter)
	if err != nil {
		return nil, err
	}
	if !hasLimit(opts) {
		opts = append(opts, options.Find().SetLimit(DefaultFindLimit))
	}
	return r.FindByCondition(ctx, clean, opts...)
}
