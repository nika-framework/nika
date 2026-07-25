package repository

import (
	"strings"
	"testing"
)

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Accepted shapes.
		{"simple", "id", false},
		{"underscore prefix", "_internal", false},
		{"digits after letter", "column1", false},
		{"mixed case", "createdAt", false},
		{"snake case", "created_at", false},
		{"qualified table.column", "users.id", false},
		{"qualified schema.table.column", "app.users.id", false},
		{"max length", strings.Repeat("a", 64), false},

		// Injection payloads.
		{"tautology", "1=1 OR x", true},
		{"tautology no digits leading", "x=1 OR x", true},
		{"statement terminator", "id; DROP TABLE t", true},
		{"escaped quote comment", `col"; --`, true},
		{"backtick payload", "col`; DROP TABLE t; -- ", true},
		{"backtick alone", "`", true},
		{"double quote alone", `"`, true},
		{"single quote", "col'", true},
		{"nul byte", "col\x00umn", true},
		{"nul byte terminator", "id\x00", true},
		{"comment marker", "id--", true},
		{"block comment", "id/*x*/", true},
		{"whitespace", "id name", true},
		{"newline", "id\nname", true},
		{"tab", "id\tname", true},
		{"parenthesis", "count(*)", true},
		{"subquery", "(SELECT 1)", true},
		{"star", "*", true},
		{"union", "id UNION SELECT", true},
		{"percent", "id%", true},
		{"semicolon only", ";", true},
		{"backslash", `col\`, true},

		// Structural rejects.
		{"empty", "", true},
		{"leading digit", "1col", true},
		{"too long", strings.Repeat("a", 65), true},
		{"empty qualified part", "users..id", true},
		{"trailing dot", "users.", true},
		{"leading dot", ".id", true},
		{"too many parts", "a.b.c.d", true},

		// Unicode: outside the ASCII allowlist, including homoglyphs and RTL
		// override characters that make a name look like a different one.
		{"unicode letter", "naïve", true},
		{"cyrillic homoglyph", "ідentity", true},
		{"emoji", "col🙂", true},
		{"fullwidth", "ｉd", true},
		{"rtl override", "id‮xx", true},
		{"zero width space", "id​name", true},

		// Reserved keywords that carry statement structure.
		{"reserved select", "select", true},
		{"reserved SELECT upper", "SELECT", true},
		{"reserved mixed case", "SeLeCt", true},
		{"reserved or", "or", true},
		{"reserved drop", "drop", true},
		{"reserved qualified part", "users.select", true},
		{"reserved information_schema", "information_schema", true},
		{"reserved pg_sleep", "pg_sleep", true},
		{"not reserved name", "name", false},
		{"not reserved status", "status", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIdentifier(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateIdentifier(%q) = nil, want error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateIdentifier(%q) = %v, want nil", tc.input, err)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	cases := []struct {
		dialect Dialect
		input   string
		want    string
	}{
		{DialectPostgres, "id", `"id"`},
		{DialectSQLite, "id", `"id"`},
		{DialectMySQL, "id", "`id`"},

		// An embedded delimiter is doubled, per the SQL standard. Validation
		// rejects these before they get here — this pins the escaping anyway,
		// because quoting is the second layer of the defence.
		{DialectPostgres, `a"b`, `"a""b"`},
		{DialectMySQL, "a`b", "`a``b`"},
		{DialectPostgres, `"`, `""""`},
		{DialectMySQL, "`", "````"},

		// The other dialect's delimiter is not special.
		{DialectPostgres, "a`b", "\"a`b\""},
		{DialectMySQL, `a"b`, "`a\"b`"},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect)+"/"+tc.input, func(t *testing.T) {
			if got := tc.dialect.QuoteIdentifier(tc.input); got != tc.want {
				t.Errorf("QuoteIdentifier(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestQuoteQualified(t *testing.T) {
	cases := []struct {
		dialect Dialect
		input   string
		want    string
	}{
		{DialectPostgres, "users", `"users"`},
		{DialectPostgres, "public.users", `"public"."users"`},
		{DialectPostgres, "app.public.users", `"app"."public"."users"`},
		{DialectMySQL, "app.users", "`app`.`users`"},
		{DialectSQLite, "main.users", `"main"."users"`},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect)+"/"+tc.input, func(t *testing.T) {
			if got := tc.dialect.QuoteQualified(tc.input); got != tc.want {
				t.Errorf("QuoteQualified(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestQuoteValidatedRejectsPayloads(t *testing.T) {
	payloads := []string{
		"1=1 OR x",
		"id; DROP TABLE t",
		`col"; --`,
		"col`",
		"col\x00",
		"naïve",
		"",
	}

	for _, d := range []Dialect{DialectPostgres, DialectMySQL, DialectSQLite} {
		for _, payload := range payloads {
			if _, err := d.QuoteValidated(payload); err == nil {
				t.Errorf("%s: QuoteValidated(%q) = nil error, want rejection", d, payload)
			}
		}
	}
}

func TestMustQuotePanicsOnBadIdentifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected mustQuote to panic on an invalid identifier")
		}
	}()
	DialectPostgres.mustQuote("table name", "users; DROP TABLE t")
}
