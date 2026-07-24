package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

// Pages runs the given pipeline with $facet-based pagination.
// Page is 1-indexed to stay consistent with the SQL repository — passing 0
// or negative is coerced to 1.
func (r *BaseRepository[T]) Pages(
	ctx context.Context,
	pipeline []any,
	page int64,
	count int64,
) (*PaginationResult, error) {

	if page < 1 {
		page = 1
	}
	if count <= 0 {
		count = 15
	}

	skip := (page - 1) * count

	// If the pipeline does not include a $sort stage, inject a deterministic
	// sort by _id ascending so pagination is stable across pages.
	if !containsStage(pipeline, "$sort") {
		pipeline = append(pipeline, bson.M{"$sort": bson.M{"_id": 1}})
	}

	aggregate := append(
		pipeline,
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
