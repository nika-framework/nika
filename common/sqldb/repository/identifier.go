package repository

import (
	"fmt"
	"strings"
)

// maxIdentifierLength is the smallest of the three supported engines' limits
// (PostgreSQL truncates at 63 bytes, MySQL allows 64). Rejecting anything
// longer keeps a name from silently meaning a different column on one engine
// than on another.
const maxIdentifierLength = 64

// maxQualifiedParts allows db.schema.table (SQL Server style) and
// schema.table / table.column, but nothing deeper — a longer chain is far more
// likely to be an injection attempt than a real object reference.
const maxQualifiedParts = 3

// reservedIdentifiers are the keywords that carry statement structure. A caller
// that supplies one as a "column" is either injecting or has a bug: quoting
// alone would make `Filter{"or": 1}` harmless, but a bare keyword arriving from
// request data is a signal worth failing on rather than executing. The list is
// deliberately limited to tokens that split or redirect a statement; ordinary
// type names and functions are omitted so real schemas keep working.
var reservedIdentifiers = map[string]struct{}{
	"all": {}, "alter": {}, "and": {}, "any": {}, "as": {}, "asc": {},
	"begin": {}, "benchmark": {}, "between": {}, "by": {},
	"case": {}, "cast": {}, "commit": {}, "conflict": {}, "create": {},
	"database": {}, "declare": {}, "delete": {}, "desc": {}, "distinct": {},
	"do": {}, "drop": {}, "dumpfile": {},
	"except": {}, "exec": {}, "execute": {}, "exists": {},
	"fetch": {}, "for": {}, "from": {},
	"grant": {}, "group": {}, "having": {},
	"ilike": {}, "in": {}, "index": {}, "information_schema": {},
	"insert": {}, "intersect": {}, "into": {}, "is": {},
	"join": {}, "lateral": {}, "like": {}, "limit": {}, "load_file": {},
	"lock": {}, "not": {}, "nowait": {}, "null": {},
	"offset": {}, "on": {}, "or": {}, "order": {}, "outfile": {}, "over": {},
	"partition": {}, "pg_sleep": {}, "procedure": {},
	"recursive": {}, "rename": {}, "returning": {}, "revoke": {}, "rollback": {},
	"savepoint": {}, "schema": {}, "select": {}, "set": {}, "shutdown": {},
	"sleep": {}, "some": {},
	"table": {}, "then": {}, "trigger": {}, "truncate": {},
	"union": {}, "update": {}, "using": {},
	"values": {}, "view": {},
	"waitfor": {}, "when": {}, "where": {}, "window": {}, "with": {},
	"xp_cmdshell": {},
}

// ValidateIdentifier reports whether name is safe to interpolate into a
// statement as a table, column, or alias.
//
// Identifiers cannot be bound as parameters, so every identifier that reaches
// SQL text has to be proven safe instead. The rule is an allowlist —
// [A-Za-z_][A-Za-z0-9_]* — which by construction excludes quotes, semicolons,
// whitespace, comment markers, NUL bytes, and every non-ASCII code point, so
// payloads such as `1=1 OR name`, `id; DROP TABLE users --`, or `col"; --`
// are rejected before any quoting happens.
//
// A dotted name (schema.table, table.column, db.schema.table) is accepted and
// each part validated independently.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("invalid identifier: empty")
	}

	parts := strings.Split(name, ".")
	if len(parts) > maxQualifiedParts {
		return fmt.Errorf("invalid identifier %q: at most %d dot-separated parts allowed", name, maxQualifiedParts)
	}

	for _, part := range parts {
		if err := validateIdentifierPart(part); err != nil {
			if len(parts) == 1 {
				return err
			}
			return fmt.Errorf("invalid identifier %q: %w", name, err)
		}
	}
	return nil
}

func validateIdentifierPart(part string) error {
	if part == "" {
		return fmt.Errorf("invalid identifier: empty part")
	}
	if len(part) > maxIdentifierLength {
		return fmt.Errorf("invalid identifier %q: longer than %d characters", part, maxIdentifierLength)
	}

	for i := 0; i < len(part); i++ {
		c := part[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			// A leading digit would be ambiguous with a numeric literal.
			if i == 0 {
				return fmt.Errorf("invalid identifier %q: must not start with a digit", part)
			}
		default:
			return fmt.Errorf("invalid identifier %q: only letters, digits, and underscore are allowed", part)
		}
	}

	if _, reserved := reservedIdentifiers[strings.ToLower(part)]; reserved {
		return fmt.Errorf("invalid identifier %q: reserved SQL keyword", part)
	}
	return nil
}

// quoteChar returns the identifier delimiter for the dialect. MySQL uses
// backticks unless ANSI_QUOTES is on, which we do not assume.
func (d Dialect) quoteChar() byte {
	if d == DialectMySQL {
		return '`'
	}
	return '"'
}

// QuoteIdentifier wraps name in the dialect's identifier delimiter, doubling
// any embedded delimiter as the SQL standard requires.
//
// Quoting is a second layer, not the defence: a lone `"` or backtick would be
// escaped into something syntactically valid but semantically wrong, and a NUL
// byte can truncate the statement inside some client libraries. Always pair
// this with ValidateIdentifier — QuoteValidated does both.
func (d Dialect) QuoteIdentifier(name string) string {
	q := normalizeDialect(d).quoteChar()
	var b strings.Builder
	b.Grow(len(name) + 2)
	b.WriteByte(q)
	for i := 0; i < len(name); i++ {
		if name[i] == q {
			b.WriteByte(q)
		}
		b.WriteByte(name[i])
	}
	b.WriteByte(q)
	return b.String()
}

// QuoteQualified quotes each dot-separated part of name individually, so
// "public.users" becomes "public"."users" rather than one identifier that
// happens to contain a dot.
func (d Dialect) QuoteQualified(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = d.QuoteIdentifier(part)
	}
	return strings.Join(parts, ".")
}

// QuoteValidated validates name and returns its quoted form. Every code path
// that puts a caller-supplied identifier into SQL text must go through here.
func (d Dialect) QuoteValidated(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return d.QuoteQualified(name), nil
}

// mustQuote is for identifiers fixed at construction time (table name, ID
// column, struct `db` tags). A bad value there is a programming error, so it
// must fail at startup rather than on a request.
func (d Dialect) mustQuote(kind, name string) string {
	quoted, err := d.QuoteValidated(name)
	if err != nil {
		panic(fmt.Sprintf("nika/sqldb: %s %q is not a valid SQL identifier: %v", kind, name, err))
	}
	return quoted
}
