package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

func (r *BaseRepository[T]) Pages(
	ctx context.Context,
	pipeline []any,
	page int64,
	count int64,
) (*PaginationResult, error) {

	if page < 0 {
		page = 0
	}

	if count <= 0 {
		count = 15
	}

	skip := page * count

	aggregate := append(
		pipeline,
		bson.M{
			"$facet": bson.M{
				"metadata": bson.A{
					bson.M{
						"$count": "total",
					},
				},
				"data": bson.A{
					bson.M{
						"$skip": skip,
					},
					bson.M{
						"$limit": count,
					},
				},
			},
		},
		bson.M{
			"$project": bson.M{
				"data": 1,
				"total": bson.M{
					"$arrayElemAt": bson.A{
						"$metadata.total",
						0,
					},
				},
			},
		},
	)

	result, err := r.FindWithAggregate(
		ctx,
		aggregate,
	)

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

	total := int64(0)

	if v, ok := row["total"].(int32); ok {
		total = int64(v)
	}

	if v, ok := row["total"].(int64); ok {
		total = v
	}

	data := []map[string]any{}

	if d, ok := row["data"].(bson.A); ok {

		for _, item := range d {

			if m, ok := item.(map[string]any); ok {
				data = append(data, m)
			}

			if m, ok := item.(bson.M); ok {
				data = append(data, m)
			}
		}
	}

	return &PaginationResult{
		Data:  data,
		Total: total,
	}, nil
}
