package repository

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// BaseRepository provides generic CRUD operations for SQL databases.
// T is the model type and ID is the primary key type (int64, string, uuid, etc.).
type BaseRepository[T any, ID comparable] struct {
	DB        *sql.DB
	TableName string
	IDColumn  string // Primary key column name (default: "id")
	Dialect   Dialect

	// Cached reflection metadata – computed once on construction.
	columns    []string
	dbTagMap   map[string]int // db tag → struct field index
	insertCols []string       // columns excluding auto-increment ID
	autoID     bool
}

// NewBaseRepository creates a new BaseRepository with pre-computed struct metadata.
// tableName: the SQL table name.
// idColumn: the primary key column name (e.g., "id").
// autoIncrementID: if true, the ID column is excluded from INSERT statements.
func NewBaseRepository[T any, ID comparable](
	db *sql.DB,
	tableName string,
	idColumn string,
	autoIncrementID bool,
) *BaseRepository[T, ID] {
	return NewBaseRepositoryWithDialect[T, ID](
		db,
		DialectPostgres,
		tableName,
		idColumn,
		autoIncrementID,
	)
}

// NewBaseRepositoryWithDialect creates a repository that emits syntax for the
// configured PostgreSQL, MySQL, or SQLite connection.
func NewBaseRepositoryWithDialect[T any, ID comparable](
	db *sql.DB,
	dialect Dialect,
	tableName string,
	idColumn string,
	autoIncrementID bool,
) *BaseRepository[T, ID] {
	if idColumn == "" {
		idColumn = "id"
	}

	repo := &BaseRepository[T, ID]{
		DB:        db,
		TableName: tableName,
		IDColumn:  idColumn,
		Dialect:   normalizeDialect(dialect),
		dbTagMap:  make(map[string]int),
		autoID:    autoIncrementID,
	}

	// Pre-compute struct field metadata via reflection (done once).
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}

		// Handle tags like `db:"name,omitempty"`
		colName := tag
		if idx := strings.IndexByte(tag, ','); idx >= 0 {
			colName = tag[:idx]
		}

		repo.columns = append(repo.columns, colName)
		repo.dbTagMap[colName] = i

		if autoIncrementID && colName == idColumn {
			continue
		}
		repo.insertCols = append(repo.insertCols, colName)
	}

	return repo
}

// getStructValues extracts field values from a struct based on the given column names.
func (r *BaseRepository[T, ID]) getStructValues(data *T, cols []string) []any {
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	values := make([]any, 0, len(cols))
	for _, col := range cols {
		if idx, ok := r.dbTagMap[col]; ok {
			values = append(values, v.Field(idx).Interface())
		}
	}
	return values
}

// scanRow scans a single database row into the model struct T.
func (r *BaseRepository[T, ID]) scanRow(row *sql.Row) (*T, error) {
	var result T
	v := reflect.ValueOf(&result).Elem()

	ptrs := make([]any, 0, len(r.columns))
	for _, col := range r.columns {
		if idx, ok := r.dbTagMap[col]; ok {
			ptrs = append(ptrs, v.Field(idx).Addr().Interface())
		}
	}

	if err := row.Scan(ptrs...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

// scanRows scans multiple database rows into a slice of model structs.
func (r *BaseRepository[T, ID]) scanRows(rows *sql.Rows) ([]T, error) {
	defer rows.Close()

	var results []T

	for rows.Next() {
		var item T
		v := reflect.ValueOf(&item).Elem()

		ptrs := make([]any, 0, len(r.columns))
		for _, col := range r.columns {
			if idx, ok := r.dbTagMap[col]; ok {
				ptrs = append(ptrs, v.Field(idx).Addr().Interface())
			}
		}

		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		results = append(results, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return results, nil
}

// columnsString returns a comma-separated list of all column names.
func (r *BaseRepository[T, ID]) columnsString() string {
	return strings.Join(r.columns, ", ")
}

// buildWhere constructs a WHERE clause and arguments from a filter map.
// Returns the WHERE clause string (including "WHERE") and the argument slice.
func (r *BaseRepository[T, ID]) buildWhere(filter Filter, startIdx int) (string, []any) {
	if len(filter) == 0 {
		return "", nil
	}

	conditions := make([]string, 0, len(filter))
	args := make([]any, 0, len(filter))
	idx := startIdx

	for col, val := range filter {
		if val == nil {
			conditions = append(conditions, fmt.Sprintf("%s IS NULL", col))
			continue
		}
		idx++
		conditions = append(conditions, fmt.Sprintf("%s = %s", col, r.Dialect.placeholder(idx)))
		args = append(args, val)
	}

	return "WHERE " + strings.Join(conditions, " AND "), args
}

// buildSetClause constructs a SET clause for UPDATE statements.
func (r *BaseRepository[T, ID]) buildSetClause(data Filter, startIdx int) (string, []any) {
	setParts := make([]string, 0, len(data))
	args := make([]any, 0, len(data))
	idx := startIdx

	for col, val := range data {
		idx++
		setParts = append(setParts, fmt.Sprintf("%s = %s", col, r.Dialect.placeholder(idx)))
		args = append(args, val)
	}

	return strings.Join(setParts, ", "), args
}

func (r *BaseRepository[T, ID]) setGeneratedID(data *T, id int64) {
	if !r.autoID {
		return
	}

	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	v = v.Elem()
	idx, ok := r.dbTagMap[r.IDColumn]
	if !ok {
		return
	}
	field := v.Field(idx)
	value := reflect.ValueOf(id)
	if field.CanSet() && value.Type().ConvertibleTo(field.Type()) {
		field.Set(value.Convert(field.Type()))
	}
}

func (r *BaseRepository[T, ID]) modelID(data *T) (ID, bool) {
	var zero ID
	if data == nil {
		return zero, false
	}

	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return zero, false
	}
	v = v.Elem()
	idx, ok := r.dbTagMap[r.IDColumn]
	if !ok {
		return zero, false
	}
	id, ok := v.Field(idx).Interface().(ID)
	return id, ok
}
