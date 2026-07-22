package repository

import (
	"context"
	"database/sql"
)

// Filter represents a set of WHERE conditions as column-value pairs.
// For simple equality: Filter{"name": "John", "age": 30}
// For advanced queries, use WhereClause directly.
type Filter = map[string]any

// OrderBy represents a sorting directive.
type OrderBy struct {
	Column string
	Desc   bool
}

// PaginationResult holds paginated query results.
type PaginationResult[T any] struct {
	Data       []T   `json:"data"`
	Total      int64 `json:"total"`
	Page       int64 `json:"page"`
	PerPage    int64 `json:"perPage"`
	TotalPages int64 `json:"totalPages"`
}

// IBaseRepository defines the contract for SQL repository operations.
type IBaseRepository[T any, ID comparable] interface {
	// Create inserts a new record and returns it with the generated ID.
	Create(ctx context.Context, data *T) (*T, error)

	// CreateTx inserts a new record within a transaction.
	CreateTx(ctx context.Context, tx *sql.Tx, data *T) (*T, error)

	// InsertMany inserts multiple records in a single batch.
	InsertMany(ctx context.Context, data []T) (int64, error)

	// FindOneByID retrieves a single record by its primary key.
	FindOneByID(ctx context.Context, id ID) (*T, error)

	// FindOne retrieves the first record matching the given filter.
	FindOne(ctx context.Context, filter Filter) (*T, error)

	// FindByCondition retrieves all records matching the given filter.
	FindByCondition(ctx context.Context, filter Filter) ([]T, error)

	// FindAll retrieves all records, optionally filtered.
	FindAll(ctx context.Context, filter Filter) ([]T, error)

	// ExistsByID checks if a record with the given ID exists.
	ExistsByID(ctx context.Context, id ID) (bool, error)

	// ExistsByCondition checks if any record matches the given filter.
	ExistsByCondition(ctx context.Context, filter Filter) (bool, error)

	// CountByCondition counts records matching the given filter.
	CountByCondition(ctx context.Context, filter Filter) (int64, error)

	// UpdateOneByID updates a single record by its primary key.
	UpdateOneByID(ctx context.Context, id ID, data Filter) error

	// UpdateOne updates the first record matching the filter.
	UpdateOne(ctx context.Context, filter Filter, data Filter) error

	// UpdateAndFindOne updates a record and returns the updated version.
	UpdateAndFindOne(ctx context.Context, filter Filter, data Filter) (*T, error)

	// UpdateMany updates all records matching the filter.
	UpdateMany(ctx context.Context, filter Filter, data Filter) (int64, error)

	// Increment increases a numeric column by the given value.
	Increment(ctx context.Context, filter Filter, column string, value int64) error

	// Decrement decreases a numeric column by the given value.
	Decrement(ctx context.Context, filter Filter, column string, value int64) error

	// DeleteByID deletes a record by its primary key.
	DeleteByID(ctx context.Context, id ID) error

	// DeleteOne deletes the first record matching the filter.
	DeleteOne(ctx context.Context, filter Filter) error

	// DeleteMany deletes all records matching the filter.
	DeleteMany(ctx context.Context, filter Filter) (int64, error)

	// Pages returns paginated results.
	Pages(ctx context.Context, filter Filter, page int64, perPage int64, orderBy ...OrderBy) (*PaginationResult[T], error)

	// RawQuery executes a raw SQL query and scans results into the model slice.
	RawQuery(ctx context.Context, query string, args ...any) ([]T, error)

	// RawExec executes a raw SQL statement (INSERT, UPDATE, DELETE).
	RawExec(ctx context.Context, query string, args ...any) (sql.Result, error)
}
