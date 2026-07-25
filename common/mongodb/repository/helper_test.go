package repository

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestToLikeRegexEscapesMetacharacters(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"plain", "ada", "ada"},
		{"dot", "ada.lovelace", `ada\.lovelace`},
		{"anchor", "^admin", `\^admin`},
		{"wildcard", ".*", `\.\*`},
		// The pattern a user would send to burn CPU; escaped it is a literal.
		{"backtracking payload", "(a+)+", `\(a\+\)\+`},
		{"character class", "[a-z]", `\[a-z\]`},
		{"alternation", "a|b", `a\|b`},
		{"repetition", "a{1,999}", `a\{1,999\}`},
		{"backslash", `a\b`, `a\\b`},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToLikeRegex(tc.query)
			if got["$regex"] != tc.want {
				t.Errorf("ToLikeRegex(%q)[$regex] = %q, want %q", tc.query, got["$regex"], tc.want)
			}
			if got["$options"] != "i" {
				t.Errorf("$options = %v, want \"i\"", got["$options"])
			}
		})
	}
}

func TestToLikeRegexOutputIsAcceptedBySanitizer(t *testing.T) {
	// The whole point of escaping: whatever a user types, the resulting filter
	// must still pass the sanitizer.
	for _, query := range []string{"(a+)+", ".*.*.*", "^admin", "a{99999}"} {
		if _, err := SanitizeUserFilter(Filter{"name": ToLikeRegex(query)}); err != nil {
			t.Errorf("SanitizeUserFilter(ToLikeRegex(%q)) = %v, want nil", query, err)
		}
	}
}

func TestToLikeRegexRaw(t *testing.T) {
	got, err := ToLikeRegexRaw("^ada")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Raw means unescaped: the caller's pattern is preserved.
	if got["$regex"] != "^ada" {
		t.Errorf("$regex = %v, want ^ada", got["$regex"])
	}

	if _, err := ToLikeRegexRaw("(a+)+"); err == nil {
		t.Error("ToLikeRegexRaw should reject a backtracking pattern")
	}
}

func TestParseObjectID(t *testing.T) {
	valid := primitive.NewObjectID()

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid hex", valid.Hex(), false},
		{"uppercase hex", "507F1F77BCF86CD799439011", false},
		{"empty", "", true},
		{"too short", "abc", true},
		{"too long", valid.Hex() + "00", true},
		{"non hex", "zzzzzzzzzzzzzzzzzzzzzzzz", true},
		{"injection attempt", `{"$ne": null}`, true},
		{"nul byte", "507f1f77bcf86cd7994390\x0011", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseObjectID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseObjectID(%q) = nil error, want rejection", tc.input)
				}
				if got != primitive.NilObjectID {
					t.Errorf("ParseObjectID(%q) returned %v alongside an error, want NilObjectID", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseObjectID(%q) = %v", tc.input, err)
			}
		})
	}

	if got, _ := ParseObjectID(valid.Hex()); got != valid {
		t.Errorf("round trip mismatch: %v != %v", got, valid)
	}
}

func TestGetSafeString(t *testing.T) {
	m := map[string]any{"name": "Ada", "age": 36, "nil": nil}

	cases := []struct {
		key  string
		want string
	}{
		{"name", "Ada"},
		{"age", ""},     // wrong type
		{"nil", ""},     // explicit nil
		{"missing", ""}, // absent
	}
	for _, tc := range cases {
		if got := GetSafeString(m, tc.key); got != tc.want {
			t.Errorf("GetSafeString(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestGetSafeBool(t *testing.T) {
	m := map[string]any{"yes": true, "no": false, "str": "true"}

	cases := []struct {
		key  string
		want bool
	}{
		{"yes", true},
		{"no", false},
		{"str", false},     // wrong type
		{"missing", false}, // absent
	}
	for _, tc := range cases {
		if got := GetSafeBool(m, tc.key); got != tc.want {
			t.Errorf("GetSafeBool(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestGetSafeDateReturnsZeroOnMiss(t *testing.T) {
	when := time.Date(2024, 5, 21, 12, 30, 45, 0, time.UTC)
	m := map[string]any{"created_at": when, "wrong": "2024-05-21", "nil": nil}

	if got := GetSafeDate(m, "created_at"); !got.Equal(when) {
		t.Errorf("GetSafeDate(present) = %v, want %v", got, when)
	}

	// A "safe" getter must not invent the current time for missing data: an
	// absent timestamp has to stay distinguishable from a real one.
	for _, key := range []string{"missing", "wrong", "nil"} {
		if got := GetSafeDate(m, key); !got.IsZero() {
			t.Errorf("GetSafeDate(%q) = %v, want the zero time", key, got)
		}
	}
}

func TestGetSafeDateOr(t *testing.T) {
	when := time.Date(2024, 5, 21, 12, 30, 45, 0, time.UTC)
	fallback := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	m := map[string]any{"created_at": when, "wrong": 5}

	if got := GetSafeDateOr(m, "created_at", fallback); !got.Equal(when) {
		t.Errorf("GetSafeDateOr(present) = %v, want %v", got, when)
	}
	for _, key := range []string{"missing", "wrong"} {
		if got := GetSafeDateOr(m, key, fallback); !got.Equal(fallback) {
			t.Errorf("GetSafeDateOr(%q) = %v, want the fallback %v", key, got, fallback)
		}
	}
}

func TestSetInsertedID(t *testing.T) {
	oid := primitive.NewObjectID()

	t.Run("ID primitive.ObjectID", func(t *testing.T) {
		type doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.ID != oid {
			t.Errorf("ID = %v, want %v", d.ID, oid)
		}
	})

	t.Run("Id string gets the hex form", func(t *testing.T) {
		type doc struct {
			Id string `bson:"_id"`
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.Id != oid.Hex() {
			t.Errorf("Id = %q, want %q", d.Id, oid.Hex())
		}
	})

	t.Run("ID any", func(t *testing.T) {
		type doc struct {
			ID any `bson:"_id"`
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.ID != any(oid) {
			t.Errorf("ID = %v, want %v", d.ID, oid)
		}
	})

	t.Run("field named ID without a tag", func(t *testing.T) {
		type doc struct {
			ID primitive.ObjectID
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.ID != oid {
			t.Errorf("ID = %v, want %v", d.ID, oid)
		}
	})

	t.Run("ObjectID name", func(t *testing.T) {
		type doc struct {
			ObjectID primitive.ObjectID
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.ObjectID != oid {
			t.Errorf("ObjectID = %v, want %v", d.ObjectID, oid)
		}
	})

	t.Run("tag with options", func(t *testing.T) {
		type doc struct {
			Key primitive.ObjectID `bson:"_id,omitempty"`
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.Key != oid {
			t.Errorf("Key = %v, want %v", d.Key, oid)
		}
	})

	t.Run("no id field is a no-op", func(t *testing.T) {
		type doc struct {
			Name string `bson:"name"`
		}
		d := &doc{Name: "Ada"}
		setInsertedID(d, oid)
		if d.Name != "Ada" {
			t.Errorf("unrelated field was modified: %q", d.Name)
		}
	})

	t.Run("unexported id field is skipped", func(t *testing.T) {
		type doc struct {
			id   primitive.ObjectID //nolint:unused // deliberately unexported
			Name string             `bson:"name"`
		}
		d := &doc{Name: "Ada"}
		// Reflection cannot set an unexported field; this must not panic.
		setInsertedID(d, oid)
		if d.id != primitive.NilObjectID {
			t.Error("an unexported field should not be set")
		}
	})

	t.Run("non-pointer is a no-op", func(t *testing.T) {
		type doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		d := doc{}
		setInsertedID(d, oid)
		if d.ID != primitive.NilObjectID {
			t.Error("a non-pointer argument should not be modified")
		}
	})

	t.Run("nil pointer is a no-op", func(t *testing.T) {
		type doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		var d *doc
		setInsertedID(d, oid)
	})

	t.Run("nil inserted id is a no-op", func(t *testing.T) {
		type doc struct {
			ID any `bson:"_id"`
		}
		d := &doc{}
		// reflect.ValueOf(nil) is invalid and Set would panic on it.
		setInsertedID(d, nil)
		if d.ID != nil {
			t.Errorf("ID = %v, want nil", d.ID)
		}
	})

	t.Run("incompatible id type is a no-op", func(t *testing.T) {
		type doc struct {
			ID chan int `bson:"_id"`
		}
		d := &doc{}
		setInsertedID(d, oid)
		if d.ID != nil {
			t.Error("an unconvertible id should be skipped")
		}
	})

	t.Run("string id from a non-ObjectID is skipped", func(t *testing.T) {
		type doc struct {
			ID string `bson:"_id"`
		}
		d := &doc{}
		setInsertedID(d, 42)
		if d.ID != "" {
			t.Errorf("ID = %q, want empty", d.ID)
		}
	})

	t.Run("non-struct pointer is a no-op", func(t *testing.T) {
		s := "x"
		setInsertedID(&s, oid)
		if s != "x" {
			t.Errorf("value = %q, want unchanged", s)
		}
	})
}
