package repository

import (
	"context"
	"database/sql"
)

// Filter represents a set of WHERE conditions as column-value pairs, combined
// with AND. A nil value means IS NULL, since `= NULL` is never true in SQL.
//
//	Filter{"name": "John", "age": 30}   →  "age" = $1 AND "name" = $2
//	Filter{"deleted_at": nil}           →  "deleted_at" IS NULL
//
// Keys are column identifiers. They cannot be bound as parameters, so each one
// is validated against a strict allowlist and quoted before it reaches SQL; a
// key such as "1=1 OR name" or "id; DROP TABLE users --" makes the call return
// an error instead of executing. Keys are also sorted, so the same filter always
// produces the same SQL text and the database's plan cache can do its job.
//
// Filter only expresses equality and IS NULL. For ranges, IN, LIKE, and negation
// use []Cond with the *ByWhere methods — the operator there is a typed enum, so
// it can never arrive from request data as a string.
type Filter = map[string]any

// OrderBy represents a sorting directive. Column is validated and quoted;
// Desc is mapped to a constant ASC/DESC keyword rather than interpolated.
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
//
// Read methods report "no row" as (nil, nil) rather than an error. Mutating
// methods reject an empty filter with ErrEmptyFilter, because an UPDATE or DELETE
// without a predicate rewrites or empties the whole table; the *Unsafe variants
// exist for when that is the intent.
type IBaseRepository[T any, ID comparable] interface {
	// Create inserts a new record and returns it with the generated ID.
	Create(ctx context.Context, data *T) (*T, error)

	// CreateTx inserts a new record within a transaction.
	CreateTx(ctx context.Context, tx *sql.Tx, data *T) (*T, error)

	// InsertMany inserts multiple records in batches.
	InsertMany(ctx context.Context, data []T) (int64, error)

	// FindOneByID retrieves a single record by its primary key.
	FindOneByID(ctx context.Context, id ID) (*T, error)

	// FindOne retrieves the first record matching the given filter.
	FindOne(ctx context.Context, filter Filter) (*T, error)

	// FindOneByWhere retrieves the first record matching the typed conditions.
	FindOneByWhere(ctx context.Context, conds []Cond) (*T, error)

	// FindByCondition retrieves all records matching the given filter.
	FindByCondition(ctx context.Context, filter Filter) ([]T, error)

	// FindByWhere retrieves all records matching the typed conditions.
	FindByWhere(ctx context.Context, conds []Cond) ([]T, error)

	// FindAll retrieves all records, optionally filtered.
	FindAll(ctx context.Context, filter Filter) ([]T, error)

	// ExistsByID checks if a record with the given ID exists.
	ExistsByID(ctx context.Context, id ID) (bool, error)

	// ExistsByCondition checks if any record matches the given filter.
	ExistsByCondition(ctx context.Context, filter Filter) (bool, error)

	// CountByCondition counts records matching the given filter.
	CountByCondition(ctx context.Context, filter Filter) (int64, error)

	// CountByWhere counts records matching the typed conditions.
	CountByWhere(ctx context.Context, conds []Cond) (int64, error)

	// UpdateOneByID updates a single record by its primary key.
	UpdateOneByID(ctx context.Context, id ID, data Filter) error

	// UpdateOne updates the first record matching the filter.
	UpdateOne(ctx context.Context, filter Filter, data Filter) error

	// UpdateAndFindOne updates a record and returns the updated version.
	UpdateAndFindOne(ctx context.Context, filter Filter, data Filter) (*T, error)

	// UpdateMany updates all records matching the filter.
	UpdateMany(ctx context.Context, filter Filter, data Filter) (int64, error)

	// UpdateByWhere updates all records matching the typed conditions.
	UpdateByWhere(ctx context.Context, conds []Cond, data Filter) (int64, error)

	// UpdateAllUnsafe updates every row in the table.
	UpdateAllUnsafe(ctx context.Context, data Filter) (int64, error)

	// Increment increases a numeric column by the given value.
	Increment(ctx context.Context, filter Filter, column string, value int64) error

	// Decrement decreases a numeric column by the given value.
	Decrement(ctx context.Context, filter Filter, column string, value int64) error

	// Upsert inserts or, on unique-constraint conflict, updates.
	Upsert(ctx context.Context, data *T, conflictColumns ...string) (*T, error)

	// DeleteByID deletes a record by its primary key.
	DeleteByID(ctx context.Context, id ID) error

	// DeleteOne deletes the first record matching the filter.
	DeleteOne(ctx context.Context, filter Filter) error

	// DeleteMany deletes all records matching the filter.
	DeleteMany(ctx context.Context, filter Filter) (int64, error)

	// DeleteByWhere deletes all records matching the typed conditions.
	DeleteByWhere(ctx context.Context, conds []Cond) (int64, error)

	// DeleteAllUnsafe deletes every row in the table.
	DeleteAllUnsafe(ctx context.Context) (int64, error)

	// Pages returns paginated results. Count and page are separate round trips;
	// use PagesTx when Total and Data must agree exactly.
	Pages(ctx context.Context, filter Filter, page int64, perPage int64, orderBy ...OrderBy) (*PaginationResult[T], error)

	// PagesTx is Pages inside the caller's transaction.
	PagesTx(ctx context.Context, tx *sql.Tx, filter Filter, page int64, perPage int64, orderBy ...OrderBy) (*PaginationResult[T], error)

	// PagesByWhere paginates using the typed condition list.
	PagesByWhere(ctx context.Context, conds []Cond, page int64, perPage int64, orderBy ...OrderBy) (*PaginationResult[T], error)

	// KeysetPage returns a page via cursor pagination, which stays O(limit) at
	// any depth unlike Pages' OFFSET.
	KeysetPage(ctx context.Context, filter Filter, after *ID, limit int64, desc bool) (*KeysetResult[T, ID], error)

	// RawQuery executes a raw SQL query and scans results into the model slice.
	// The query is not validated or escaped — see the method doc.
	RawQuery(ctx context.Context, query string, args ...any) ([]T, error)

	// RawExec executes a raw SQL statement (INSERT, UPDATE, DELETE).
	// The query is not validated or escaped — see the method doc.
	RawExec(ctx context.Context, query string, args ...any) (sql.Result, error)
}
