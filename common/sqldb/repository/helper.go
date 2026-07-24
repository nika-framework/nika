package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// NullString creates a sql.NullString from a regular string.
// Empty strings are treated as NULL.
func NullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// NullInt64 creates a sql.NullInt64. Zero values are treated as NULL — use
// NullInt64Ptr when zero is a legitimate value that must be stored as 0.
func NullInt64(i int64) sql.NullInt64 {
	if i == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: i, Valid: true}
}

// NullInt64Ptr treats nil as NULL and preserves 0 as a real value.
func NullInt64Ptr(i *int64) sql.NullInt64 {
	if i == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *i, Valid: true}
}

// NullFloat64 creates a sql.NullFloat64. Zero values are treated as NULL — use
// NullFloat64Ptr when zero is a legitimate value that must be stored as 0.
func NullFloat64(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

// NullFloat64Ptr treats nil as NULL and preserves 0 as a real value.
func NullFloat64Ptr(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}

// NullBool creates a sql.NullBool.
func NullBool(b bool) sql.NullBool {
	return sql.NullBool{Bool: b, Valid: true}
}

// NullTime creates a sql.NullTime. Zero time is treated as NULL.
func NullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// ToLikePattern wraps a string in SQL LIKE wildcards.
// e.g. "john" → "%john%"
func ToLikePattern(query string) string {
	return fmt.Sprintf("%%%s%%", query)
}

// ToStartsWith creates a SQL LIKE pattern for prefix matching.
// e.g. "john" → "john%"
func ToStartsWith(query string) string {
	return fmt.Sprintf("%s%%", query)
}

// ToEndsWith creates a SQL LIKE pattern for suffix matching.
// e.g. "john" → "%john"
func ToEndsWith(query string) string {
	return fmt.Sprintf("%%%s", query)
}

// InClause generates a PostgreSQL parameterized IN clause for safe SQL queries.
// Use InClauseForDialect when the query targets MySQL or SQLite.
func InClause[T any](column string, startIdx int, values []T) (string, []any) {
	return InClauseForDialect(DialectPostgres, column, startIdx, values)
}

// InClauseForDialect generates an IN clause using the placeholders required by
// the selected PostgreSQL, MySQL, or SQLite driver.
func InClauseForDialect[T any](dialect Dialect, column string, startIdx int, values []T) (string, []any) {
	if len(values) == 0 {
		return "1 = 0", nil // Always false condition
	}
	dialect = normalizeDialect(dialect)

	placeholderParts := make([]string, len(values))
	args := make([]any, len(values))

	for i, v := range values {
		placeholderParts[i] = dialect.placeholder(startIdx + i)
		args[i] = v
	}

	clause := fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholderParts, ", "))
	return clause, args
}
