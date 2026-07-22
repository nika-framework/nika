package repository

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// Pages returns paginated results with total count and metadata.
func (r *BaseRepository[T, ID]) Pages(
	ctx context.Context,
	filter Filter,
	page int64,
	perPage int64,
	orderBy ...OrderBy,
) (*PaginationResult[T], error) {

	if page < 1 {
		page = 1
	}

	if perPage <= 0 {
		perPage = 15
	}

	offset := (page - 1) * perPage

	// Count total records
	total, err := r.CountByCondition(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("pagination count error: %w", err)
	}

	if total == 0 {
		return &PaginationResult[T]{
			Data:       []T{},
			Total:      0,
			Page:       page,
			PerPage:    perPage,
			TotalPages: 0,
		}, nil
	}

	// Build the query
	whereClause, args := r.buildWhere(filter, 0)

	// Build ORDER BY clause
	orderClause := ""
	if len(orderBy) > 0 {
		orderParts := make([]string, 0, len(orderBy))
		for _, o := range orderBy {
			dir := "ASC"
			if o.Desc {
				dir = "DESC"
			}
			orderParts = append(orderParts, fmt.Sprintf("%s %s", o.Column, dir))
		}
		orderClause = "ORDER BY " + strings.Join(orderParts, ", ")
	} else {
		orderClause = fmt.Sprintf("ORDER BY %s ASC", r.IDColumn)
	}

	nextIdx := len(args) + 1

	query := fmt.Sprintf(
		"SELECT %s FROM %s %s %s LIMIT $%d OFFSET $%d",
		r.columnsString(),
		r.TableName,
		whereClause,
		orderClause,
		nextIdx,
		nextIdx+1,
	)

	args = append(args, perPage, offset)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pagination query error: %w", err)
	}

	data, err := r.scanRows(rows)
	if err != nil {
		return nil, err
	}

	totalPages := int64(math.Ceil(float64(total) / float64(perPage)))

	return &PaginationResult[T]{
		Data:       data,
		Total:      total,
		Page:       page,
		PerPage:    perPage,
		TotalPages: totalPages,
	}, nil
}
