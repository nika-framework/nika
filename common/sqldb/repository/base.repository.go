package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ErrEmptyFilter is returned by the mutating helpers when the caller supplies no
// predicate. An UPDATE or DELETE with no WHERE clause rewrites or empties the
// whole table, which is almost never what a request handler meant; use
// UpdateAllUnsafe / DeleteAllUnsafe to say so explicitly.
var ErrEmptyFilter = errors.New("repository: refusing to run an unfiltered statement (use the *Unsafe variant if this is intentional)")

// ErrNoUpdateColumns is returned when an UPDATE is asked to set nothing, which
// would produce `SET ` and a syntax error.
var ErrNoUpdateColumns = errors.New("repository: update requires at least one column")

// ErrNotFound is returned when a write that must produce a row (INSERT ...
// RETURNING) comes back empty.
var ErrNotFound = errors.New("repository: no row returned")

// BaseRepository provides generic CRUD operations for SQL databases.
// T is the model struct type (not a pointer) and ID is the primary key type
// (int64, string, uuid, etc.).
//
// Identifiers (table, columns, ORDER BY targets) can never be bound as
// parameters, so every one of them is validated against an allowlist and quoted
// before it reaches SQL text. Identifiers fixed by the model are checked once
// here, at construction, and the checked+quoted forms are cached; identifiers
// that arrive per call (filter keys, SET keys, sort columns) are validated on
// each use and surface as an error rather than a panic.
type BaseRepository[T any, ID comparable] struct {
	DB        *sql.DB
	TableName string
	IDColumn  string // Primary key column name (default: "id")
	Dialect   Dialect

	// Cached reflection + quoting metadata – computed once on construction.
	columns    []string
	dbTagMap   map[string]int // db tag → struct field index
	fieldIdx   []int          // struct field index per entry in columns
	insertCols []string       // columns excluding auto-increment ID
	insertIdx  []int          // struct field index per entry in insertCols
	autoID     bool

	quotedTable    string
	quotedID       string
	quotedColumns  string   // "a", "b", "c" ready for SELECT
	quotedInsert   []string // quoted insertCols, positionally aligned
	quotedByColumn map[string]string
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
//
// It panics when T is not a struct or when the table name, ID column, or any
// `db` tag is not a valid SQL identifier: those are compile-time facts about the
// model, so a mistake must fail at startup instead of once per request.
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
	d := normalizeDialect(dialect)

	// reflect.TypeOf(zero) returns nil for an interface T and yields a nil
	// pointer for a pointer T, both of which used to panic deep inside
	// NumField/Field. Taking the type of *T instead works for every T.
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() == reflect.Ptr {
		panic(fmt.Sprintf(
			"nika/sqldb: model type must be a struct, not a pointer (%s) — use NewBaseRepository[%s] and pass *%s to Create",
			t, t.Elem(), t.Elem(),
		))
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("nika/sqldb: model type must be a struct, got %s (%s)", t, t.Kind()))
	}

	repo := &BaseRepository[T, ID]{
		DB:             db,
		TableName:      tableName,
		IDColumn:       idColumn,
		Dialect:        d,
		dbTagMap:       make(map[string]int),
		quotedByColumn: make(map[string]string),
		autoID:         autoIncrementID,
	}

	repo.quotedTable = d.mustQuote("table name", tableName)
	repo.quotedID = d.mustQuote("id column", idColumn)

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

		quoted := d.mustQuote(fmt.Sprintf("db tag on %s.%s", t.Name(), field.Name), colName)
		if _, dup := repo.dbTagMap[colName]; dup {
			panic(fmt.Sprintf("nika/sqldb: duplicate db tag %q on %s", colName, t))
		}

		repo.columns = append(repo.columns, colName)
		repo.fieldIdx = append(repo.fieldIdx, i)
		repo.dbTagMap[colName] = i
		repo.quotedByColumn[colName] = quoted

		if autoIncrementID && colName == idColumn {
			continue
		}
		repo.insertCols = append(repo.insertCols, colName)
		repo.insertIdx = append(repo.insertIdx, i)
		repo.quotedInsert = append(repo.quotedInsert, quoted)
	}

	if len(repo.columns) == 0 {
		panic(fmt.Sprintf("nika/sqldb: %s has no exported fields with a `db` tag", t))
	}

	quotedAll := make([]string, len(repo.columns))
	for i, col := range repo.columns {
		quotedAll[i] = repo.quotedByColumn[col]
	}
	repo.quotedColumns = strings.Join(quotedAll, ", ")

	return repo
}

// quote validates and quotes a caller-supplied identifier, preferring the form
// cached at construction when the column belongs to the model.
func (r *BaseRepository[T, ID]) quote(name string) (string, error) {
	if quoted, ok := r.quotedByColumn[name]; ok {
		return quoted, nil
	}
	return r.Dialect.QuoteValidated(name)
}

// getValues extracts field values from a struct by pre-computed field index.
func (r *BaseRepository[T, ID]) getValues(data *T, fieldIdx []int) []any {
	v := reflect.ValueOf(data).Elem()

	values := make([]any, len(fieldIdx))
	for i, idx := range fieldIdx {
		values[i] = v.Field(idx).Interface()
	}
	return values
}

// scanTargets returns the addressable field pointers for a row scan. The field
// indices are pre-computed, so the per-row cost is one bounds-checked Field call
// per column instead of a map lookup — this is the read hot path.
func (r *BaseRepository[T, ID]) scanTargets(v reflect.Value) []any {
	ptrs := make([]any, len(r.fieldIdx))
	for i, idx := range r.fieldIdx {
		ptrs[i] = v.Field(idx).Addr().Interface()
	}
	return ptrs
}

// scanRow scans a single row into T, reporting "not found" as (nil, nil).
//
// That contract belongs to the Find* methods, where a missing row is an ordinary
// outcome the caller checks for with `if result == nil`. Writes must not use it:
// see scanRowRequired.
func (r *BaseRepository[T, ID]) scanRow(row *sql.Row) (*T, error) {
	var result T
	v := reflect.ValueOf(&result).Elem()

	if err := row.Scan(r.scanTargets(v)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &result, nil
}

// scanRowRequired scans a row that must exist. INSERT ... RETURNING and
// upserts go through here: with scanRow's semantics a write that produced no row
// (an ON CONFLICT DO NOTHING that hit the conflict, a RETURNING filtered away)
// returned (nil, nil) and looked like success to the caller.
func (r *BaseRepository[T, ID]) scanRowRequired(row *sql.Row) (*T, error) {
	var result T
	v := reflect.ValueOf(&result).Elem()

	if err := row.Scan(r.scanTargets(v)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: statement affected no rows", ErrNotFound)
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

		if err := rows.Scan(r.scanTargets(v)...); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		results = append(results, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return results, nil
}

// columnsString returns the quoted, comma-separated list of all columns.
func (r *BaseRepository[T, ID]) columnsString() string {
	return r.quotedColumns
}

// filterToConds converts the map-based Filter API into the typed condition list.
//
// The keys are sorted because ranging a Go map is randomised: the same filter
// produced a different SQL string on every call, which defeated the server-side
// prepared-statement and plan caches and made query logs impossible to group.
func filterToConds(filter Filter) []Cond {
	if len(filter) == 0 {
		return nil
	}
	keys := make([]string, 0, len(filter))
	for k := range filter {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	conds := make([]Cond, 0, len(keys))
	for _, k := range keys {
		// A nil value means IS NULL; `= NULL` never matches.
		if filter[k] == nil {
			conds = append(conds, Cond{Column: k, Op: OpIsNull})
			continue
		}
		conds = append(conds, Cond{Column: k, Op: OpEq, Value: filter[k]})
	}
	return conds
}

// buildWhere constructs a WHERE clause and arguments from a filter map.
// Returns the clause including the "WHERE" keyword, the argument slice, and an
// error when any filter key is not a valid identifier.
func (r *BaseRepository[T, ID]) buildWhere(filter Filter, startIdx int) (string, []any, error) {
	clause, args, _, err := buildWhereConds(r.Dialect, filterToConds(filter), startIdx)
	return clause, args, err
}

// buildWhereNext is buildWhere plus the last placeholder index consumed, for
// statements that append more placeholders (LIMIT/OFFSET).
func (r *BaseRepository[T, ID]) buildWhereNext(filter Filter, startIdx int) (string, []any, int, error) {
	return buildWhereConds(r.Dialect, filterToConds(filter), startIdx)
}

// buildSetClause constructs a SET clause for UPDATE statements. Column names are
// validated and quoted; values are always bound.
func (r *BaseRepository[T, ID]) buildSetClause(data Filter, startIdx int) (string, []any, error) {
	if len(data) == 0 {
		return "", nil, ErrNoUpdateColumns
	}

	// Sorted for the same plan-cache and log-readability reason as buildWhere.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	setParts := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	idx := startIdx

	for _, col := range keys {
		quoted, err := r.quote(col)
		if err != nil {
			return "", nil, fmt.Errorf("update: %w", err)
		}
		idx++
		setParts = append(setParts, fmt.Sprintf("%s = %s", quoted, r.Dialect.placeholder(idx)))
		args = append(args, data[col])
	}

	return strings.Join(setParts, ", "), args, nil
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

// joinSQL joins statement fragments with single spaces, dropping the empty
// ones. Without it an absent WHERE clause left a double space in the statement,
// which is harmless to the engine but makes generated SQL impossible to compare
// or grep for.
func joinSQL(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// concatArgs joins two argument lists into a fresh slice.
//
// `append(setArgs, whereArgs...)` writes into setArgs' backing array whenever it
// has spare capacity, so the caller's slice can be mutated behind its back. It
// is safe only as long as buildSetClause keeps returning an exactly-sized slice,
// which is not a property any caller should have to rely on.
func concatArgs(first, second []any) []any {
	out := make([]any, 0, len(first)+len(second))
	out = append(out, first...)
	out = append(out, second...)
	return out
}
