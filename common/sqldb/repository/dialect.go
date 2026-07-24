package repository

import (
	"strconv"
	"strings"
)

// Dialect identifies the SQL syntax used by a repository connection.
type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectMySQL    Dialect = "mysql"
	DialectSQLite   Dialect = "sqlite3"
)

func normalizeDialect(dialect Dialect) Dialect {
	switch Dialect(strings.ToLower(strings.TrimSpace(string(dialect)))) {
	case DialectMySQL:
		return DialectMySQL
	case DialectSQLite, "sqlite":
		return DialectSQLite
	default:
		return DialectPostgres
	}
}

func (d Dialect) placeholder(index int) string {
	if d == DialectPostgres {
		return "$" + strconv.Itoa(index)
	}
	return "?"
}

func (d Dialect) placeholders(start, count int) string {
	parts := make([]string, count)
	for i := 0; i < count; i++ {
		parts[i] = d.placeholder(start + i)
	}
	return strings.Join(parts, ", ")
}

func (d Dialect) supportsReturning() bool {
	return d == DialectPostgres
}
