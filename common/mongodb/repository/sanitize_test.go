package repository

import (
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestSanitizeFilterRejectsOperators(t *testing.T) {
	cases := []struct {
		name   string
		filter Filter
	}{
		// The authentication bypass: a JSON body turning an equality check into
		// "any document where the field exists".
		{"top level $ne", Filter{"password": bson.M{"$ne": nil}}},
		{"top level $gt", Filter{"password": bson.M{"$gt": ""}}},
		{"$regex", Filter{"email": bson.M{"$regex": ".*"}}},
		{"$exists", Filter{"password": bson.M{"$exists": true}}},
		{"$in", Filter{"role": bson.M{"$in": bson.A{"admin"}}}},
		{"$nin", Filter{"role": bson.M{"$nin": bson.A{"user"}}}},

		// Server-side execution.
		{"$where", Filter{"$where": "this.password.length > 0"}},
		{"$expr", Filter{"$expr": bson.M{"$eq": bson.A{"$a", "$b"}}}},
		{"$function", Filter{"$function": bson.M{"body": "function(){}"}}},
		{"$accumulator", Filter{"$accumulator": bson.M{}}},
		{"$jsonSchema", Filter{"$jsonSchema": bson.M{}}},

		// Logical combinators restructure the query.
		{"$or", Filter{"$or": bson.A{bson.M{"a": 1}}}},
		{"$and", Filter{"$and": bson.A{bson.M{"a": 1}}}},
		{"$nor", Filter{"$nor": bson.A{bson.M{"a": 1}}}},
		{"$not nested", Filter{"age": bson.M{"$not": bson.M{"$gt": 5}}}},

		// Nesting depth must not hide an operator.
		{"deeply nested", Filter{"a": bson.M{"b": bson.M{"c": bson.M{"$ne": nil}}}}},
		{"plain map nesting", Filter{"a": map[string]any{"$ne": nil}}},
		{"bson.D nesting", Filter{"a": bson.D{{Key: "$ne", Value: nil}}}},
		{"array of maps", Filter{"a": bson.A{bson.M{"$ne": 1}}}},
		{"any slice of maps", Filter{"a": []any{map[string]any{"$gt": 1}}}},
		{"array of arrays", Filter{"a": bson.A{bson.A{bson.M{"$where": "x"}}}}},

		// Malformed keys.
		{"empty key", Filter{"": 1}},
		{"nul in key", Filter{"na\x00me": 1}},
		{"nested empty key", Filter{"a": bson.M{"": 1}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SanitizeFilter(tc.filter); err == nil {
				t.Fatalf("SanitizeFilter(%v) = nil error, want rejection", tc.filter)
			}
		})
	}
}

func TestSanitizeFilterAcceptsPlainEquality(t *testing.T) {
	cases := []struct {
		name   string
		filter Filter
	}{
		{"nil filter", nil},
		{"empty filter", Filter{}},
		{"scalars", Filter{"email": "a@b.c", "age": 36, "active": true}},
		{"dotted path", Filter{"profile.name": "Ada"}},
		{"object id", Filter{"_id": primitive.NewObjectID()}},
		{"nested document", Filter{"profile": bson.M{"name": "Ada", "city": "London"}}},
		{"array of scalars", Filter{"tags": bson.A{"a", "b"}}},
		{"any slice of scalars", Filter{"tags": []any{"a", "b"}}},
		{"bson.D of scalars", Filter{"profile": bson.D{{Key: "name", Value: "Ada"}}}},
		{"explicit nil value", Filter{"deleted_at": nil}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := SanitizeFilter(tc.filter)
			if err != nil {
				t.Fatalf("SanitizeFilter(%v) = %v, want nil", tc.filter, err)
			}
			if out == nil {
				t.Fatal("SanitizeFilter returned a nil filter")
			}
			if len(out) != len(tc.filter) {
				t.Errorf("length = %d, want %d", len(out), len(tc.filter))
			}
		})
	}
}

func TestSanitizeFilterDoesNotMutateInput(t *testing.T) {
	nested := bson.M{"name": "Ada"}
	filter := Filter{"profile": nested, "age": 36}

	if _, err := SanitizeFilter(filter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filter) != 2 || len(nested) != 1 {
		t.Error("SanitizeFilter mutated its input")
	}
}

func TestSanitizeUserFilterAllowlist(t *testing.T) {
	cases := []struct {
		name    string
		filter  Filter
		wantErr bool
	}{
		{"$in allowed", Filter{"role": bson.M{"$in": bson.A{"a", "b"}}}, false},
		{"$gt allowed", Filter{"age": bson.M{"$gt": 18}}, false},
		{"$gte allowed", Filter{"age": bson.M{"$gte": 18}}, false},
		{"$lt allowed", Filter{"age": bson.M{"$lt": 65}}, false},
		{"$lte allowed", Filter{"age": bson.M{"$lte": 65}}, false},
		{"$ne allowed", Filter{"status": bson.M{"$ne": "banned"}}, false},
		{"range combination", Filter{"age": bson.M{"$gte": 18, "$lte": 65}}, false},
		{"$regex allowed", Filter{"name": bson.M{"$regex": "^ada"}}, false},

		// Not on the allowlist.
		{"$exists rejected", Filter{"password": bson.M{"$exists": true}}, true},
		{"$nin rejected", Filter{"role": bson.M{"$nin": bson.A{"a"}}}, true},
		{"$or rejected", Filter{"$or": bson.A{bson.M{"a": 1}}}, true},
		{"$where rejected", Filter{"$where": "x"}, true},
		{"$expr rejected", Filter{"$expr": bson.M{}}, true},
		{"$elemMatch rejected", Filter{"tags": bson.M{"$elemMatch": bson.M{"a": 1}}}, true},
		{"$mod rejected", Filter{"n": bson.M{"$mod": bson.A{2, 0}}}, true},

		// A top-level operator is never a field name.
		{"$in at top level rejected", Filter{"$in": bson.A{1}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SanitizeUserFilter(tc.filter)
			if tc.wantErr && err == nil {
				t.Fatalf("SanitizeUserFilter(%v) = nil error, want rejection", tc.filter)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("SanitizeUserFilter(%v) = %v, want nil", tc.filter, err)
			}
		})
	}
}

func TestSanitizeUserFilterCustomAllowlist(t *testing.T) {
	// A caller narrowing the set to equality-plus-$in.
	if _, err := SanitizeUserFilter(Filter{"role": bson.M{"$in": bson.A{"a"}}}, "$in"); err != nil {
		t.Fatalf("$in with a custom allowlist = %v, want nil", err)
	}
	if _, err := SanitizeUserFilter(Filter{"age": bson.M{"$gt": 1}}, "$in"); err == nil {
		t.Fatal("$gt outside the custom allowlist should be rejected")
	}

	// Forbidden operators cannot be allowlisted back in.
	for _, op := range []string{"$where", "$function", "$expr", "$accumulator"} {
		if _, err := SanitizeUserFilter(Filter{op: "x"}, op); err == nil {
			t.Errorf("%s was accepted after being allowlisted; it must stay forbidden", op)
		}
	}
}

func TestSanitizeUserFilterRegexRules(t *testing.T) {
	cases := []struct {
		name    string
		filter  Filter
		wantErr bool
	}{
		{"simple anchor", Filter{"name": bson.M{"$regex": "^ada"}}, false},
		{"escaped literal", Filter{"name": bson.M{"$regex": `ada\.lovelace`}}, false},
		{"single quantifier", Filter{"name": bson.M{"$regex": "a+b"}}, false},
		{"character class quantified", Filter{"name": bson.M{"$regex": "[abc]+"}}, false},

		// Catastrophic backtracking shapes.
		{"nested quantifier", Filter{"name": bson.M{"$regex": "(a+)+"}}, true},
		{"nested star", Filter{"name": bson.M{"$regex": "(a*)*"}}, true},
		{"alternation in quantified group", Filter{"name": bson.M{"$regex": "(a|aa)+"}}, true},
		{"quantified group with bound", Filter{"name": bson.M{"$regex": "(a+){2}"}}, true},
		{"lookahead", Filter{"name": bson.M{"$regex": "(?=a)b"}}, true},
		{"negative lookahead", Filter{"name": bson.M{"$regex": "(?!a)b"}}, true},
		{"lookbehind", Filter{"name": bson.M{"$regex": "(?<=a)b"}}, true},
		{"backreference", Filter{"name": bson.M{"$regex": `(a)\1`}}, true},
		{"huge repetition", Filter{"name": bson.M{"$regex": "a{99999}"}}, true},
		{"nul byte", Filter{"name": bson.M{"$regex": "a\x00b"}}, true},
		{"too long", Filter{"name": bson.M{"$regex": strings.Repeat("a", MaxRegexLength+1)}}, true},
		{"non-string pattern", Filter{"name": bson.M{"$regex": 42}}, true},
		{"non-string options", Filter{"name": bson.M{"$regex": "a", "$options": 1}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SanitizeUserFilter(tc.filter)
			if tc.wantErr && err == nil {
				t.Fatalf("SanitizeUserFilter(%v) = nil error, want rejection", tc.filter)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("SanitizeUserFilter(%v) = %v, want nil", tc.filter, err)
			}
		})
	}
}

func TestSanitizeRegexOptionsStripsDangerousFlags(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"i", "i"},
		{"m", "m"},
		{"im", "im"},
		// `x` makes whitespace and comments significant; `s` makes `.` cross
		// newlines. Both silently change what a bounded-looking pattern matches.
		{"ix", "i"},
		{"is", "i"},
		{"xs", ""},
		{"imxs", "im"},
		{"u", ""},
		{"iii", "i"},
		{"", ""},
	}

	for _, tc := range cases {
		if got := sanitizeRegexOptions(tc.in); got != tc.want {
			t.Errorf("sanitizeRegexOptions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeUserFilterRewritesOptions(t *testing.T) {
	out, err := SanitizeUserFilter(Filter{"name": bson.M{"$regex": "^ada", "$options": "isx"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inner, ok := out["name"].(bson.M)
	if !ok {
		t.Fatalf("name = %#v, want bson.M", out["name"])
	}
	if inner["$options"] != "i" {
		t.Errorf("$options = %#v, want \"i\"", inner["$options"])
	}
	if inner["$regex"] != "^ada" {
		t.Errorf("$regex = %#v", inner["$regex"])
	}
}

func TestSanitizeDropsOrphanOptions(t *testing.T) {
	// $options without $regex is a server error, so it is dropped rather than
	// forwarded.
	out, err := SanitizeUserFilter(Filter{"name": bson.M{"$options": "i"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inner, ok := out["name"].(bson.M)
	if !ok {
		t.Fatalf("name = %#v", out["name"])
	}
	if _, present := inner["$options"]; present {
		t.Errorf("orphan $options survived: %#v", inner)
	}
}

func TestSanitizePrimitiveRegexValue(t *testing.T) {
	out, err := SanitizeFilter(Filter{"name": primitive.Regex{Pattern: "^ada", Options: "isx"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rx, ok := out["name"].(primitive.Regex)
	if !ok {
		t.Fatalf("name = %#v, want primitive.Regex", out["name"])
	}
	if rx.Options != "i" {
		t.Errorf("options = %q, want \"i\"", rx.Options)
	}

	if _, err := SanitizeFilter(Filter{"name": primitive.Regex{Pattern: "(a+)+"}}); err == nil {
		t.Error("a backtracking primitive.Regex should be rejected")
	}
}

func TestSanitizePreservesShape(t *testing.T) {
	in := Filter{
		"age":     bson.M{"$gte": 18},
		"tags":    bson.A{"a", "b"},
		"profile": bson.D{{Key: "city", Value: "London"}},
		"name":    "Ada",
	}
	out, err := SanitizeUserFilter(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := out["age"].(bson.M); !ok {
		t.Errorf("age lost its bson.M type: %T", out["age"])
	}
	if _, ok := out["tags"].(bson.A); !ok {
		t.Errorf("tags lost its bson.A type: %T", out["tags"])
	}
	if _, ok := out["profile"].(bson.D); !ok {
		t.Errorf("profile lost its bson.D type: %T", out["profile"])
	}
	if out["name"] != "Ada" {
		t.Errorf("name = %#v", out["name"])
	}
	if !reflect.DeepEqual(out["tags"], bson.A{"a", "b"}) {
		t.Errorf("tags = %#v", out["tags"])
	}
}

func TestValidateRegexPattern(t *testing.T) {
	cases := []struct {
		pattern string
		wantErr bool
	}{
		{"", false},
		{"^ada$", false},
		{"a.b", false},
		{`\(literal\)`, false},
		{"[0-9]{1,5}", false},
		{"(abc)+", false},    // no inner quantifier or alternation
		{`(a\+)+`, false},    // the + is escaped, so it is a literal
		{"[a+b]+", false},    // + inside a class is a literal
		{"(a+)+", true},      // classic exponential
		{"((a)*)*", true},    // nested through a group
		{"(a|b|ab)*", true},  // alternation under a quantifier
		{"a{200}", true},     // above maxRepetition
		{"a{1,200}", true},   // the upper bound counts too
		{"(?:a+)+", true},    // non-capturing does not help
		{`(x)\1`, true},      // backreference
		{"(?=x)", true},      // lookahead
		{"a\x00", true},      // NUL
		{"(a+)*(b+)*", true}, // first offender is enough
		{"[(a+)]+", false},   // parens inside a class are literals
	}

	for _, tc := range cases {
		err := ValidateRegexPattern(tc.pattern)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateRegexPattern(%q) = nil, want error", tc.pattern)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateRegexPattern(%q) = %v, want nil", tc.pattern, err)
		}
	}
}

func TestValidateRegexPatternLengthBoundary(t *testing.T) {
	if err := ValidateRegexPattern(strings.Repeat("a", MaxRegexLength)); err != nil {
		t.Errorf("a pattern of exactly MaxRegexLength should be accepted: %v", err)
	}
	if err := ValidateRegexPattern(strings.Repeat("a", MaxRegexLength+1)); err == nil {
		t.Error("a pattern one over MaxRegexLength should be rejected")
	}
}
