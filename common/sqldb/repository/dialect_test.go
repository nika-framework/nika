package repository

import "testing"

func TestDialectPlaceholders(t *testing.T) {
	cases := []struct {
		dialect     Dialect
		placeholder string
		list        string
		returning   bool
	}{
		{DialectPostgres, "$3", "$1, $2, $3", true},
		{DialectMySQL, "?", "?, ?, ?", false},
		{DialectSQLite, "?", "?, ?, ?", false},
	}

	for _, test := range cases {
		if got := test.dialect.placeholder(3); got != test.placeholder {
			t.Errorf("%s placeholder = %q, want %q", test.dialect, got, test.placeholder)
		}
		if got := test.dialect.placeholders(1, 3); got != test.list {
			t.Errorf("%s placeholders = %q, want %q", test.dialect, got, test.list)
		}
		if got := test.dialect.supportsReturning(); got != test.returning {
			t.Errorf("%s supportsReturning = %v, want %v", test.dialect, got, test.returning)
		}
	}
}

func TestInClauseForDialect(t *testing.T) {
	values := []string{"active", "pending"}
	postgresClause, postgresArgs := InClauseForDialect(DialectPostgres, "status", 1, values)
	if postgresClause != "status IN ($1, $2)" || len(postgresArgs) != 2 {
		t.Errorf("PostgreSQL IN clause = %q, args = %v", postgresClause, postgresArgs)
	}

	mysqlClause, mysqlArgs := InClauseForDialect(DialectMySQL, "status", 1, values)
	if mysqlClause != "status IN (?, ?)" || len(mysqlArgs) != 2 {
		t.Errorf("MySQL IN clause = %q, args = %v", mysqlClause, mysqlArgs)
	}
}
