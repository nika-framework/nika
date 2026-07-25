package repository

import (
	"reflect"
	"testing"
)

func TestBuildConds(t *testing.T) {
	cases := []struct {
		name     string
		dialect  Dialect
		conds    []Cond
		startIdx int
		want     string
		wantArgs []any
		wantIdx  int
	}{
		{
			name:     "no conditions",
			dialect:  DialectPostgres,
			conds:    nil,
			want:     "",
			wantArgs: nil,
			wantIdx:  0,
		},
		{
			name:     "equality",
			dialect:  DialectPostgres,
			conds:    []Cond{Eq("name", "ada")},
			want:     `"name" = $1`,
			wantArgs: []any{"ada"},
			wantIdx:  1,
		},
		{
			name:    "all scalar operators",
			dialect: DialectPostgres,
			conds: []Cond{
				NotEq("name", "x"),
				GT("age", 1),
				GTE("age", 2),
				LT("age", 3),
				LTE("age", 4),
				Like("email", "%@b.c"),
			},
			want:     `"name" <> $1 AND "age" > $2 AND "age" >= $3 AND "age" < $4 AND "age" <= $5 AND "email" LIKE $6`,
			wantArgs: []any{"x", 1, 2, 3, 4, "%@b.c"},
			wantIdx:  6,
		},
		{
			name:     "null checks consume no placeholder",
			dialect:  DialectPostgres,
			conds:    []Cond{IsNull("email"), Eq("name", "ada"), NotNull("age")},
			want:     `"email" IS NULL AND "name" = $1 AND "age" IS NOT NULL`,
			wantArgs: []any{"ada"},
			wantIdx:  1,
		},
		{
			name:     "nil equality becomes IS NULL",
			dialect:  DialectPostgres,
			conds:    []Cond{Eq("email", nil), Eq("name", "ada")},
			want:     `"email" IS NULL AND "name" = $1`,
			wantArgs: []any{"ada"},
			wantIdx:  1,
		},
		{
			name:     "nil inequality becomes IS NOT NULL",
			dialect:  DialectPostgres,
			conds:    []Cond{NotEq("email", nil)},
			want:     `"email" IS NOT NULL`,
			wantArgs: nil,
			wantIdx:  0,
		},
		{
			// `IN ()` is a syntax error everywhere, so an empty set degenerates to
			// a constant false.
			name:     "OpIn with zero values",
			dialect:  DialectPostgres,
			conds:    []Cond{In("id", []int{}), Eq("name", "ada")},
			want:     `1 = 0 AND "name" = $1`,
			wantArgs: []any{"ada"},
			wantIdx:  1,
		},
		{
			name:     "OpIn with one value",
			dialect:  DialectPostgres,
			conds:    []Cond{In("id", []int{7})},
			want:     `"id" IN ($1)`,
			wantArgs: []any{7},
			wantIdx:  1,
		},
		{
			name:     "OpIn with three values",
			dialect:  DialectPostgres,
			conds:    []Cond{In("id", []int{1, 2, 3}), Eq("name", "ada")},
			want:     `"id" IN ($1, $2, $3) AND "name" = $4`,
			wantArgs: []any{1, 2, 3, "ada"},
			wantIdx:  4,
		},
		{
			name:     "OpIn with three values on mysql",
			dialect:  DialectMySQL,
			conds:    []Cond{In("id", []int{1, 2, 3}), Eq("name", "ada")},
			want:     "`id` IN (?, ?, ?) AND `name` = ?",
			wantArgs: []any{1, 2, 3, "ada"},
			wantIdx:  4,
		},
		{
			// Set semantics: nothing is excluded by "not in the empty set".
			name:     "OpNotIn with zero values",
			dialect:  DialectPostgres,
			conds:    []Cond{NotIn("id", []int{})},
			want:     "1 = 1",
			wantArgs: nil,
			wantIdx:  0,
		},
		{
			name:     "OpNotIn with two values",
			dialect:  DialectPostgres,
			conds:    []Cond{NotIn("id", []int{4, 5})},
			want:     `"id" NOT IN ($1, $2)`,
			wantArgs: []any{4, 5},
			wantIdx:  2,
		},
		{
			name:     "between",
			dialect:  DialectPostgres,
			conds:    []Cond{Between("age", 18, 65), Eq("name", "ada")},
			want:     `"age" BETWEEN $1 AND $2 AND "name" = $3`,
			wantArgs: []any{18, 65, "ada"},
			wantIdx:  3,
		},
		{
			name:     "ilike on postgres uses ILIKE",
			dialect:  DialectPostgres,
			conds:    []Cond{ILike("name", "%ada%")},
			want:     `"name" ILIKE $1`,
			wantArgs: []any{"%ada%"},
			wantIdx:  1,
		},
		{
			// MySQL and SQLite have no ILIKE, so both sides are folded to make the
			// behaviour identical rather than collation-dependent.
			name:     "ilike on mysql folds case",
			dialect:  DialectMySQL,
			conds:    []Cond{ILike("name", "%ada%")},
			want:     "LOWER(`name`) LIKE LOWER(?)",
			wantArgs: []any{"%ada%"},
			wantIdx:  1,
		},
		{
			name:     "ilike on sqlite folds case",
			dialect:  DialectSQLite,
			conds:    []Cond{ILike("name", "%ada%")},
			want:     `LOWER("name") LIKE LOWER(?)`,
			wantArgs: []any{"%ada%"},
			wantIdx:  1,
		},
		{
			name:     "start index offsets placeholders",
			dialect:  DialectPostgres,
			conds:    []Cond{Eq("name", "ada"), In("id", []int{1, 2})},
			startIdx: 3,
			want:     `"name" = $4 AND "id" IN ($5, $6)`,
			wantArgs: []any{"ada", 1, 2},
			wantIdx:  6,
		},
		{
			name:     "qualified column",
			dialect:  DialectPostgres,
			conds:    []Cond{Eq("users.name", "ada")},
			want:     `"users"."name" = $1`,
			wantArgs: []any{"ada"},
			wantIdx:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, args, idx, err := buildConds(tc.dialect, tc.conds, tc.startIdx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("clause\n got: %s\nwant: %s", got, tc.want)
			}
			if len(args) != 0 || len(tc.wantArgs) != 0 {
				if !reflect.DeepEqual(args, tc.wantArgs) {
					t.Errorf("args = %#v, want %#v", args, tc.wantArgs)
				}
			}
			if idx != tc.wantIdx {
				t.Errorf("next index = %d, want %d", idx, tc.wantIdx)
			}
		})
	}
}

func TestBuildCondsErrors(t *testing.T) {
	cases := []struct {
		name  string
		conds []Cond
	}{
		{"injected column", []Cond{{Column: "1=1 OR x", Op: OpEq, Value: 1}}},
		{"quote in column", []Cond{{Column: `name"; --`, Op: OpEq, Value: 1}}},
		{"empty column", []Cond{{Column: "", Op: OpEq, Value: 1}}},
		{"unknown operator", []Cond{{Column: "name", Op: Operator(200), Value: 1}}},
		{"operator just past enum", []Cond{{Column: "name", Op: opCount, Value: 1}}},
		{"IN with a scalar", []Cond{{Column: "id", Op: OpIn, Value: 5}}},
		{"IN with nil", []Cond{{Column: "id", Op: OpIn, Value: nil}}},
		{"BETWEEN with one bound", []Cond{{Column: "age", Op: OpBetween, Value: []any{1}}}},
		{"BETWEEN with three bounds", []Cond{{Column: "age", Op: OpBetween, Value: []any{1, 2, 3}}}},
		{"BETWEEN with a scalar", []Cond{{Column: "age", Op: OpBetween, Value: 1}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := buildConds(DialectPostgres, tc.conds, 0); err == nil {
				t.Fatalf("buildConds(%v) = nil error, want rejection", tc.conds)
			}
		})
	}
}

func TestBuildWhereCondsAddsKeyword(t *testing.T) {
	clause, _, _, err := buildWhereConds(DialectPostgres, []Cond{Eq("id", 1)}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clause != `WHERE "id" = $1` {
		t.Errorf("clause = %q", clause)
	}

	// No conditions must not yield a dangling WHERE.
	clause, _, _, err = buildWhereConds(DialectPostgres, nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clause != "" {
		t.Errorf("clause = %q, want empty", clause)
	}
}

func TestElements(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		want    []any
		wantErr bool
	}{
		{"any slice", []any{1, "a"}, []any{1, "a"}, false},
		{"typed slice", []int{1, 2}, []any{1, 2}, false},
		{"string slice", []string{"a"}, []any{"a"}, false},
		{"array", [2]int{1, 2}, []any{1, 2}, false},
		{"empty slice", []int{}, []any{}, false},
		// A []byte is one bytea/BLOB value, not a list of numbers.
		{"byte slice is scalar", []byte("hi"), []any{[]byte("hi")}, false},
		{"scalar rejected", 5, nil, true},
		{"struct rejected", struct{}{}, nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := elements(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("elements(%v) = nil error, want rejection", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("elements(%v) = %#v, want %#v", tc.value, got, tc.want)
			}
		})
	}
}

func TestOperatorString(t *testing.T) {
	cases := map[Operator]string{
		OpEq:      "=",
		OpNotEq:   "<>",
		OpGT:      ">",
		OpGTE:     ">=",
		OpLT:      "<",
		OpLTE:     "<=",
		OpLike:    "LIKE",
		OpILike:   "ILIKE",
		OpIn:      "IN",
		OpNotIn:   "NOT IN",
		OpIsNull:  "IS NULL",
		OpNotNull: "IS NOT NULL",
		OpBetween: "BETWEEN",
	}
	for op, want := range cases {
		if got := op.String(); got != want {
			t.Errorf("Operator(%d).String() = %q, want %q", uint8(op), got, want)
		}
	}
}

func TestFilterToCondsIsSorted(t *testing.T) {
	conds := filterToConds(Filter{"z": 1, "a": nil, "m": "x"})
	want := []Cond{
		{Column: "a", Op: OpIsNull},
		{Column: "m", Op: OpEq, Value: "x"},
		{Column: "z", Op: OpEq, Value: 1},
	}
	if !reflect.DeepEqual(conds, want) {
		t.Errorf("filterToConds = %#v, want %#v", conds, want)
	}

	if filterToConds(nil) != nil {
		t.Error("filterToConds(nil) should be nil")
	}
}
