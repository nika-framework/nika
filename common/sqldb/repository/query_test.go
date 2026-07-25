package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// testUser is the model for every builder test. The `db` tags are what the
// constructor validates and caches.
type testUser struct {
	ID        int64  `db:"id"`
	Name      string `db:"name"`
	Email     string `db:"email"`
	Age       int    `db:"age,omitempty"`
	ignored   string //nolint:unused // unexported: must be skipped
	Transient string `db:"-"`
	NoTag     string
}

// newTestRepo builds a repository with a nil *sql.DB. Every builder under test is
// a pure function of the model metadata and the arguments, so no database is
// needed — which is the point of extracting them.
func newTestRepo(t *testing.T, d Dialect) *BaseRepository[testUser, int64] {
	t.Helper()
	return NewBaseRepositoryWithDialect[testUser, int64](nil, d, "users", "id", true)
}

// assertSQL compares a generated statement and its arguments.
func assertSQL(t *testing.T, gotQuery string, gotArgs []any, wantQuery string, wantArgs []any) {
	t.Helper()
	if gotQuery != wantQuery {
		t.Errorf("query mismatch\n got: %s\nwant: %s", gotQuery, wantQuery)
	}
	if len(gotArgs) == 0 && len(wantArgs) == 0 {
		return
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args mismatch\n got: %#v\nwant: %#v", gotArgs, wantArgs)
	}
}

func TestConstructorMetadata(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	wantColumns := []string{"id", "name", "email", "age"}
	if !reflect.DeepEqual(repo.columns, wantColumns) {
		t.Errorf("columns = %v, want %v", repo.columns, wantColumns)
	}
	// The auto-increment ID is excluded from INSERT.
	wantInsert := []string{"name", "email", "age"}
	if !reflect.DeepEqual(repo.insertCols, wantInsert) {
		t.Errorf("insertCols = %v, want %v", repo.insertCols, wantInsert)
	}
	// Field indices must line up with the column lists so scanning can index
	// directly instead of looking up a map per row.
	if len(repo.fieldIdx) != len(repo.columns) || len(repo.insertIdx) != len(repo.insertCols) {
		t.Fatalf("index slices out of sync: fieldIdx=%v insertIdx=%v", repo.fieldIdx, repo.insertIdx)
	}
	typ := reflect.TypeOf(testUser{})
	for i, idx := range repo.fieldIdx {
		if got := typ.Field(idx).Tag.Get("db"); !hasPrefixColumn(got, repo.columns[i]) {
			t.Errorf("fieldIdx[%d] points at tag %q, want column %q", i, got, repo.columns[i])
		}
	}
	if repo.columnsString() != `"id", "name", "email", "age"` {
		t.Errorf("columnsString = %s", repo.columnsString())
	}
}

func hasPrefixColumn(tag, col string) bool {
	return tag == col || (len(tag) > len(col) && tag[:len(col)] == col && tag[len(col)] == ',')
}

func TestConstructorPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"interface model", func() {
			NewBaseRepositoryWithDialect[any, int64](nil, DialectPostgres, "users", "id", true)
		}},
		{"pointer model", func() {
			NewBaseRepositoryWithDialect[*testUser, int64](nil, DialectPostgres, "users", "id", true)
		}},
		{"non-struct model", func() {
			NewBaseRepositoryWithDialect[string, int64](nil, DialectPostgres, "users", "id", true)
		}},
		{"injected table name", func() {
			NewBaseRepositoryWithDialect[testUser, int64](nil, DialectPostgres, "users; DROP TABLE t", "id", true)
		}},
		{"injected id column", func() {
			NewBaseRepositoryWithDialect[testUser, int64](nil, DialectPostgres, "users", "id\" --", true)
		}},
		{"injected db tag", func() {
			type bad struct {
				ID int64 `db:"id) OR 1=1 --"`
			}
			NewBaseRepositoryWithDialect[bad, int64](nil, DialectPostgres, "users", "id", false)
		}},
		{"no db tags", func() {
			type bare struct{ Name string }
			NewBaseRepositoryWithDialect[bare, int64](nil, DialectPostgres, "users", "id", false)
		}},
		{"duplicate db tag", func() {
			type dup struct {
				A string `db:"name"`
				B string `db:"name"`
			}
			NewBaseRepositoryWithDialect[dup, int64](nil, DialectPostgres, "users", "id", false)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic at construction")
				}
			}()
			tc.fn()
		})
	}
}

func TestFindOneQuery(t *testing.T) {
	cases := []struct {
		dialect Dialect
		want    string
	}{
		{DialectPostgres, `SELECT "id", "name", "email", "age" FROM "users" WHERE "email" = $1 AND "name" = $2 LIMIT 1`},
		{DialectMySQL, "SELECT `id`, `name`, `email`, `age` FROM `users` WHERE `email` = ? AND `name` = ? LIMIT 1"},
		{DialectSQLite, `SELECT "id", "name", "email", "age" FROM "users" WHERE "email" = ? AND "name" = ? LIMIT 1`},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect), func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			// Two keys, deliberately not in sorted insertion order: the builder
			// sorts them, so the SQL text is stable across runs even though Go
			// randomises map iteration.
			got, args, err := repo.findOneQuery(Filter{"name": "ada", "email": "a@b.c"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSQL(t, got, args, tc.want, []any{"a@b.c", "ada"})
		})
	}
}

func TestFilterOrderIsDeterministic(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	filter := Filter{"name": "ada", "email": "a@b.c", "age": 36, "id": 7}

	first, firstArgs, err := repo.findOneQuery(filter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Repeat enough times that a randomised map range would almost certainly
	// produce a different ordering at least once.
	for i := 0; i < 100; i++ {
		got, args, err := repo.findOneQuery(filter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != first {
			t.Fatalf("iteration %d produced different SQL\n got: %s\nfirst: %s", i, got, first)
		}
		if !reflect.DeepEqual(args, firstArgs) {
			t.Fatalf("iteration %d produced different args: %v vs %v", i, args, firstArgs)
		}
	}
	if first != `SELECT "id", "name", "email", "age" FROM "users" WHERE "age" = $1 AND "email" = $2 AND "id" = $3 AND "name" = $4 LIMIT 1` {
		t.Errorf("unexpected sorted query: %s", first)
	}
}

func TestFilterInjectionIsRejected(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	payloads := []Filter{
		{"1=1 OR name": "x"},
		{"id; DROP TABLE users --": 1},
		{`name"; --`: "x"},
		{"name": "ok", "id) OR 1=1 --": 1},
		{"": "x"},
	}

	for _, filter := range payloads {
		if _, _, err := repo.findOneQuery(filter); err == nil {
			t.Errorf("findOneQuery(%v) = nil error, want rejection", filter)
		}
		if _, _, err := repo.countQuery(filter); err == nil {
			t.Errorf("countQuery(%v) = nil error, want rejection", filter)
		}
		if _, _, err := repo.deleteManyQuery(filter); err == nil {
			t.Errorf("deleteManyQuery(%v) = nil error, want rejection", filter)
		}
		if _, _, err := repo.updateManyQuery(filter, Filter{"name": "x"}); err == nil {
			t.Errorf("updateManyQuery(%v) = nil error, want rejection", filter)
		}
	}
}

func TestSetClauseInjectionIsRejected(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	for _, data := range []Filter{
		{"name = 'x', role": "admin"},
		{"name\" = 'x' --": "y"},
		{"": "y"},
	} {
		if _, _, err := repo.updateManyQuery(Filter{"id": 1}, data); err == nil {
			t.Errorf("updateManyQuery set=%v = nil error, want rejection", data)
		}
	}
}

// TestUpdateManyNilFilterValue is the placeholder-numbering regression test: a
// nil filter value renders as IS NULL and consumes no placeholder, so the next
// predicate must reuse the index it would otherwise have taken.
func TestUpdateManyNilFilterValue(t *testing.T) {
	cases := []struct {
		dialect  Dialect
		want     string
		wantArgs []any
	}{
		{
			DialectPostgres,
			`UPDATE "users" SET "email" = $1, "name" = $2 WHERE "age" = $3 AND "id" IS NULL`,
			[]any{"a@b.c", "ada", 36},
		},
		{
			DialectMySQL,
			"UPDATE `users` SET `email` = ?, `name` = ? WHERE `age` = ? AND `id` IS NULL",
			[]any{"a@b.c", "ada", 36},
		},
		{
			DialectSQLite,
			`UPDATE "users" SET "email" = ?, "name" = ? WHERE "age" = ? AND "id" IS NULL`,
			[]any{"a@b.c", "ada", 36},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect), func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			// 2 SET columns + 1 nil filter value + 1 non-nil filter value.
			got, args, err := repo.updateManyQuery(
				Filter{"id": nil, "age": 36},
				Filter{"name": "ada", "email": "a@b.c"},
			)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSQL(t, got, args, tc.want, tc.wantArgs)
		})
	}
}

func TestUpdateReturningPlaceholderNumbering(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	got, args, err := repo.updateReturningQuery(
		Filter{"id": nil, "age": 36},
		Filter{"name": "ada", "email": "a@b.c"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertSQL(t, got, args,
		`UPDATE "users" SET "email" = $1, "name" = $2 WHERE "age" = $3 AND "id" IS NULL RETURNING "id", "name", "email", "age"`,
		[]any{"a@b.c", "ada", 36},
	)
}

func TestUpdateOneQuery(t *testing.T) {
	cases := []struct {
		dialect Dialect
		want    string
	}{
		{
			DialectPostgres,
			`UPDATE "users" SET "name" = $1 WHERE "id" IN (SELECT "id" FROM "users" WHERE "email" = $2 LIMIT 1)`,
		},
		{
			// MySQL cannot reference the update target in a subquery.
			DialectMySQL,
			"UPDATE `users` SET `name` = ? WHERE `email` = ? LIMIT 1",
		},
		{
			DialectSQLite,
			`UPDATE "users" SET "name" = ? WHERE "id" IN (SELECT "id" FROM "users" WHERE "email" = ? LIMIT 1)`,
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect), func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			got, args, err := repo.updateOneQuery(Filter{"email": "a@b.c"}, Filter{"name": "ada"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSQL(t, got, args, tc.want, []any{"ada", "a@b.c"})
		})
	}
}

func TestUpdateByIDQuery(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	got, args, err := repo.updateByIDQuery(Filter{"name": "ada", "email": "a@b.c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The ID placeholder follows the SET placeholders.
	assertSQL(t, got, args,
		`UPDATE "users" SET "email" = $1, "name" = $2 WHERE "id" = $3`,
		[]any{"a@b.c", "ada"},
	)
}

func TestDeleteOneQuery(t *testing.T) {
	cases := []struct {
		dialect Dialect
		want    string
	}{
		{DialectPostgres, `DELETE FROM "users" WHERE "id" IN (SELECT "id" FROM "users" WHERE "email" = $1 LIMIT 1)`},
		{DialectMySQL, "DELETE FROM `users` WHERE `email` = ? LIMIT 1"},
		{DialectSQLite, `DELETE FROM "users" WHERE "id" IN (SELECT "id" FROM "users" WHERE "email" = ? LIMIT 1)`},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect), func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			got, args, err := repo.deleteOneQuery(Filter{"email": "a@b.c"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSQL(t, got, args, tc.want, []any{"a@b.c"})
		})
	}
}

func TestEmptyFilterIsRejected(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	if _, _, err := repo.deleteManyQuery(Filter{}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("deleteManyQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, _, err := repo.deleteOneQuery(Filter{}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("deleteOneQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, _, err := repo.updateManyQuery(Filter{}, Filter{"name": "x"}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("updateManyQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, _, err := repo.updateOneQuery(Filter{}, Filter{"name": "x"}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("updateOneQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, _, err := repo.updateReturningQuery(Filter{}, Filter{"name": "x"}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("updateReturningQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, _, err := repo.stepQuery(Filter{}, "age", "+"); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("stepQuery(empty) = %v, want ErrEmptyFilter", err)
	}
	if _, err := repo.UpdateAndFindOne(context.Background(), Filter{}, Filter{"name": "x"}); !errors.Is(err, ErrEmptyFilter) {
		t.Errorf("UpdateAndFindOne(empty) = %v, want ErrEmptyFilter", err)
	}

	// Reads with no predicate stay legal.
	if _, _, err := repo.countQuery(Filter{}); err != nil {
		t.Errorf("countQuery(empty) = %v, want nil", err)
	}
	if got, _, _ := repo.countQuery(Filter{}); got != `SELECT COUNT(*) FROM "users"` {
		t.Errorf("countQuery(empty) = %s", got)
	}
}

func TestEmptySetClauseIsRejected(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	if _, _, err := repo.updateManyQuery(Filter{"id": 1}, Filter{}); !errors.Is(err, ErrNoUpdateColumns) {
		t.Errorf("updateManyQuery with no columns = %v, want ErrNoUpdateColumns", err)
	}
	if _, _, err := repo.updateByIDQuery(Filter{}); !errors.Is(err, ErrNoUpdateColumns) {
		t.Errorf("updateByIDQuery with no columns = %v, want ErrNoUpdateColumns", err)
	}
}

func TestStepQuery(t *testing.T) {
	cases := []struct {
		dialect Dialect
		op      string
		want    string
	}{
		{DialectPostgres, "+", `UPDATE "users" SET "age" = "age" + $1 WHERE "id" = $2`},
		{DialectPostgres, "-", `UPDATE "users" SET "age" = "age" - $1 WHERE "id" = $2`},
		{DialectMySQL, "+", "UPDATE `users` SET `age` = `age` + ? WHERE `id` = ?"},
		{DialectSQLite, "-", `UPDATE "users" SET "age" = "age" - ? WHERE "id" = ?`},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect)+tc.op, func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			got, args, err := repo.stepQuery(Filter{"id": 7}, "age", tc.op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSQL(t, got, args, tc.want, []any{7})
		})
	}
}

func TestStepQueryRejectsInjectedColumn(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	// Increment's column argument was interpolated three times per statement.
	for _, col := range []string{"age = 0, role", `age" --`, "age; DROP TABLE users", "1"} {
		if _, _, err := repo.stepQuery(Filter{"id": 1}, col, "+"); err == nil {
			t.Errorf("stepQuery(column=%q) = nil error, want rejection", col)
		}
	}
}

func TestIncrementRejectsNonPositive(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	ctx := context.Background()
	if err := repo.Increment(ctx, Filter{"id": 1}, "age", 0); err == nil {
		t.Error("Increment(0) = nil error, want rejection")
	}
	if err := repo.Decrement(ctx, Filter{"id": 1}, "age", -3); err == nil {
		t.Error("Decrement(-3) = nil error, want rejection")
	}
}

func TestInsertQuery(t *testing.T) {
	cases := []struct {
		dialect Dialect
		want    string
	}{
		{DialectPostgres, `INSERT INTO "users" ("name", "email", "age") VALUES ($1, $2, $3)`},
		{DialectMySQL, "INSERT INTO `users` (`name`, `email`, `age`) VALUES (?, ?, ?)"},
		{DialectSQLite, `INSERT INTO "users" ("name", "email", "age") VALUES (?, ?, ?)`},
	}

	for _, tc := range cases {
		t.Run(string(tc.dialect), func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			if got := repo.insertQuery(); got != tc.want {
				t.Errorf("insertQuery()\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestInsertManyQuery(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)
	want := `INSERT INTO "users" ("name", "email", "age") VALUES ($1, $2, $3), ($4, $5, $6)`
	if got := repo.insertManyQuery(2); got != want {
		t.Errorf("insertManyQuery(2)\n got: %s\nwant: %s", got, want)
	}
}

func TestUpsertQuery(t *testing.T) {
	cases := []struct {
		name         string
		dialect      Dialect
		conflict     []string
		want         string
		returnsRow   bool
		expectReject bool
	}{
		{
			name:       "postgres do update",
			dialect:    DialectPostgres,
			conflict:   []string{"email"},
			want:       `INSERT INTO "users" ("name", "email", "age") VALUES ($1, $2, $3) ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`,
			returnsRow: true,
		},
		{
			name:       "sqlite do update",
			dialect:    DialectSQLite,
			conflict:   []string{"email"},
			want:       `INSERT INTO "users" ("name", "email", "age") VALUES (?, ?, ?) ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`,
			returnsRow: true,
		},
		{
			name:       "mysql duplicate key",
			dialect:    DialectMySQL,
			conflict:   []string{"email"},
			want:       "INSERT INTO `users` (`name`, `email`, `age`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `age` = VALUES(`age`)",
			returnsRow: true,
		},
		{
			name:     "postgres all columns are conflict keys",
			dialect:  DialectPostgres,
			conflict: []string{"name", "email", "age"},
			// Nothing left to update, so DO NOTHING — which returns no row on
			// conflict.
			want:       `INSERT INTO "users" ("name", "email", "age") VALUES ($1, $2, $3) ON CONFLICT ("name", "email", "age") DO NOTHING`,
			returnsRow: false,
		},
		{
			name:     "mysql all columns are conflict keys",
			dialect:  DialectMySQL,
			conflict: []string{"name", "email", "age"},
			// A self-assignment is MySQL's no-op update.
			want:       "INSERT INTO `users` (`name`, `email`, `age`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `id` = `id`",
			returnsRow: true,
		},
		{
			name:         "injected conflict column",
			dialect:      DialectPostgres,
			conflict:     []string{`email") DO UPDATE SET "role" = 'admin' --`},
			expectReject: true,
		},
		{
			name:         "no conflict columns",
			dialect:      DialectPostgres,
			conflict:     nil,
			expectReject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			got, returnsRow, err := repo.upsertQuery(tc.conflict)
			if tc.expectReject {
				if err == nil {
					t.Fatalf("upsertQuery(%v) = nil error, want rejection", tc.conflict)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("upsertQuery\n got: %s\nwant: %s", got, tc.want)
			}
			if returnsRow != tc.returnsRow {
				t.Errorf("alwaysReturnsRow = %v, want %v", returnsRow, tc.returnsRow)
			}
		})
	}
}

func TestPagesQuery(t *testing.T) {
	cases := []struct {
		name      string
		dialect   Dialect
		orderBy   []OrderBy
		wantCount string
		wantPage  string
	}{
		{
			name:      "postgres default order",
			dialect:   DialectPostgres,
			wantCount: `SELECT COUNT(*) FROM "users" WHERE "name" = $1`,
			wantPage:  `SELECT "id", "name", "email", "age" FROM "users" WHERE "name" = $1 ORDER BY "id" ASC LIMIT $2 OFFSET $3`,
		},
		{
			name:      "postgres explicit order",
			dialect:   DialectPostgres,
			orderBy:   []OrderBy{{Column: "age", Desc: true}, {Column: "name"}},
			wantCount: `SELECT COUNT(*) FROM "users" WHERE "name" = $1`,
			wantPage:  `SELECT "id", "name", "email", "age" FROM "users" WHERE "name" = $1 ORDER BY "age" DESC, "name" ASC LIMIT $2 OFFSET $3`,
		},
		{
			name:      "mysql explicit order",
			dialect:   DialectMySQL,
			orderBy:   []OrderBy{{Column: "age", Desc: true}},
			wantCount: "SELECT COUNT(*) FROM `users` WHERE `name` = ?",
			wantPage:  "SELECT `id`, `name`, `email`, `age` FROM `users` WHERE `name` = ? ORDER BY `age` DESC LIMIT ? OFFSET ?",
		},
		{
			name:      "sqlite explicit order",
			dialect:   DialectSQLite,
			orderBy:   []OrderBy{{Column: "age"}},
			wantCount: `SELECT COUNT(*) FROM "users" WHERE "name" = ?`,
			wantPage:  `SELECT "id", "name", "email", "age" FROM "users" WHERE "name" = ? ORDER BY "age" ASC LIMIT ? OFFSET ?`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t, tc.dialect)
			countQuery, pageQuery, args, err := repo.pagesQuery(Filter{"name": "ada"}, tc.orderBy)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if countQuery != tc.wantCount {
				t.Errorf("count query\n got: %s\nwant: %s", countQuery, tc.wantCount)
			}
			if pageQuery != tc.wantPage {
				t.Errorf("page query\n got: %s\nwant: %s", pageQuery, tc.wantPage)
			}
			if !reflect.DeepEqual(args, []any{"ada"}) {
				t.Errorf("where args = %v", args)
			}
		})
	}
}

func TestPagesOrderByInjectionIsRejected(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	// The classic vector: ?sort= wired straight into OrderBy.Column.
	payloads := []string{
		"id; DROP TABLE users --",
		"age DESC, (SELECT 1)",
		`name" --`,
		"1=1",
		"age ASC",
	}
	for _, col := range payloads {
		if _, _, _, err := repo.pagesQuery(Filter{}, []OrderBy{{Column: col}}); err == nil {
			t.Errorf("pagesQuery(orderBy=%q) = nil error, want rejection", col)
		}
	}
}

func TestOrderByDirectionIsConstant(t *testing.T) {
	if got := (OrderBy{Column: "id"}).direction(); got != "ASC" {
		t.Errorf("direction() = %q, want ASC", got)
	}
	if got := (OrderBy{Column: "id", Desc: true}).direction(); got != "DESC" {
		t.Errorf("direction() = %q, want DESC", got)
	}
}

func TestNormalizePageArgs(t *testing.T) {
	cases := []struct {
		page, perPage         int64
		wantPage, wantPerPage int64
	}{
		{0, 0, 1, defaultPerPage},
		{-5, -1, 1, defaultPerPage},
		{3, 20, 3, 20},
		// A perPage from a query string must not be able to ask for the table.
		{1, 100000000, 1, maxPerPage},
		{1, maxPerPage + 1, 1, maxPerPage},
	}

	for _, tc := range cases {
		page, perPage := normalizePageArgs(tc.page, tc.perPage)
		if page != tc.wantPage || perPage != tc.wantPerPage {
			t.Errorf("normalizePageArgs(%d, %d) = (%d, %d), want (%d, %d)",
				tc.page, tc.perPage, page, perPage, tc.wantPage, tc.wantPerPage)
		}
	}
}

func TestKeysetQuery(t *testing.T) {
	repo := newTestRepo(t, DialectPostgres)

	t.Run("first page", func(t *testing.T) {
		got, args, err := repo.keysetQuery(Filter{"name": "ada"}, nil, 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// limit+1 rows are fetched so HasMore needs no second query.
		assertSQL(t, got, args,
			`SELECT "id", "name", "email", "age" FROM "users" WHERE "name" = $1 ORDER BY "id" ASC LIMIT $2`,
			[]any{"ada", int64(11)},
		)
	})

	t.Run("with cursor", func(t *testing.T) {
		after := int64(42)
		got, args, err := repo.keysetQuery(Filter{"name": "ada"}, &after, 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertSQL(t, got, args,
			`SELECT "id", "name", "email", "age" FROM "users" WHERE "name" = $1 AND "id" > $2 ORDER BY "id" ASC LIMIT $3`,
			[]any{"ada", int64(42), int64(11)},
		)
	})

	t.Run("descending cursor", func(t *testing.T) {
		after := int64(42)
		got, args, err := repo.keysetQuery(nil, &after, 5, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertSQL(t, got, args,
			`SELECT "id", "name", "email", "age" FROM "users" WHERE "id" < $1 ORDER BY "id" DESC LIMIT $2`,
			[]any{int64(42), int64(6)},
		)
	})
}

func TestConcatArgsDoesNotAlias(t *testing.T) {
	// A slice with spare capacity: append would write into it in place.
	setArgs := make([]any, 2, 8)
	setArgs[0], setArgs[1] = "a", "b"

	out := concatArgs(setArgs, []any{"c"})
	out[0] = "mutated"

	if setArgs[0] != "a" {
		t.Errorf("concatArgs aliased its input: setArgs[0] = %v", setArgs[0])
	}
	if len(out) != 3 || out[2] != "c" {
		t.Errorf("concatArgs = %v", out)
	}
}

func TestJoinSQLSkipsEmptyFragments(t *testing.T) {
	if got := joinSQL("SELECT 1", "", "FROM t", ""); got != "SELECT 1 FROM t" {
		t.Errorf("joinSQL = %q", got)
	}
}

// Compile-time proof that the concrete repository still satisfies the published
// interface after the signature changes.
var _ IBaseRepository[testUser, int64] = (*BaseRepository[testUser, int64])(nil)
