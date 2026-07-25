package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

// DefaultPageSize is used when the caller passes a non-positive count.
const DefaultPageSize int64 = 15

// MaxPageSize caps how many documents one page may request.
//
// count feeds $limit directly, so an unbounded value taken from a query string
// (`?perPage=100000000`) asks the server to buffer that many documents and the
// process to decode them — a one-request memory exhaustion.
const MaxPageSize int64 = 500

// ClampPageSize normalises a requested page size into [1, MaxPageSize].
func ClampPageSize(count int64) int64 {
	if count <= 0 {
		return DefaultPageSize
	}
	if count > MaxPageSize {
		return MaxPageSize
	}
	return count
}

// Pages runs the given pipeline with $facet-based pagination.
// Page is 1-indexed to stay consistent with the SQL repository — passing 0
// or negative is coerced to 1. count is clamped to MaxPageSize.
//
// The $facet stage re-runs the upstream pipeline once for the count and once for
// the page. That is inherent to counting and paginating in a single round trip;
// the alternative is two round trips whose results can disagree.
func (r *BaseRepository[T]) Pages(
	ctx context.Context,
	pipeline []any,
	page int64,
	count int64,
) (*PaginationResult, error) {

	if page < 1 {
		page = 1
	}
	count = ClampPageSize(count)

	skip := (page - 1) * count

	// Build on a copy: appending to the caller's slice would write into its
	// backing array whenever it has spare capacity, so a reused pipeline would
	// silently grow the $sort and $facet stages on every call.
	stages := make([]any, len(pipeline), len(pipeline)+3)
	copy(stages, pipeline)

	// If the pipeline does not include a $sort stage, inject a deterministic
	// sort by _id ascending so pagination is stable across pages.
	if !containsStage(stages, "$sort") {
		stages = append(stages, bson.M{"$sort": bson.M{"_id": 1}})
	}

	aggregate := append(
		stages,
		bson.M{
			"$facet": bson.M{
				"metadata": bson.A{
					bson.M{"$count": "total"},
				},
				"data": bson.A{
					bson.M{"$skip": skip},
					bson.M{"$limit": count},
				},
			},
		},
		bson.M{
			"$project": bson.M{
				"data": 1,
				// $count emits no document at all for an empty result, so
				// $metadata is [] and $arrayElemAt yields "missing": the field is
				// absent from the output and the type switch below leaves total
				// at 0, which is the right answer.
				"total": bson.M{
					"$arrayElemAt": bson.A{"$metadata.total", 0},
				},
			},
		},
	)

	result, err := r.FindWithAggregate(ctx, aggregate)
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return &PaginationResult{
			Data:  []map[string]any{},
			Total: 0,
		}, nil
	}

	row := result[0]

	var total int64
	switch v := row["total"].(type) {
	case int32:
		total = int64(v)
	case int64:
		total = v
	case float64:
		total = int64(v)
	}

	data := []map[string]any{}

	if d, ok := row["data"].(bson.A); ok {
		for _, item := range d {
			if m, ok := item.(bson.M); ok {
				data = append(data, m)
				continue
			}
			if m, ok := item.(map[string]any); ok {
				data = append(data, m)
			}
		}
	}

	return &PaginationResult{
		Data:  data,
		Total: total,
	}, nil
}

// containsStage reports whether the aggregation pipeline already declares the
// given top-level operator (e.g. "$sort", "$match").
func containsStage(pipeline []any, op string) bool {
	for _, stage := range pipeline {
		switch s := stage.(type) {
		case bson.M:
			if _, ok := s[op]; ok {
				return true
			}
		case map[string]any:
			if _, ok := s[op]; ok {
				return true
			}
		case bson.D:
			for _, e := range s {
				if e.Key == op {
					return true
				}
			}
		}
	}
	return false
}
