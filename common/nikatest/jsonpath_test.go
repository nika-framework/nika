package nikatest

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, raw string) any {
	t.Helper()

	value, err := decodeJSONValue([]byte(raw))
	if err != nil {
		t.Fatalf("cannot decode %q: %v", raw, err)
	}
	return value
}

func TestLookupPath(t *testing.T) {
	document := decode(t, `{
		"success": true,
		"data": {
			"users": [
				{"id": 1, "email": "ada@example.com", "tags": ["a", "b"]},
				{"id": 2, "email": "grace@example.com"}
			],
			"total": 2,
			"nested": {"deep": {"value": "found"}}
		},
		"totals": {"2024": 10}
	}`)

	tests := []struct {
		path      string
		want      string
		wantFound bool
	}{
		{path: "", want: "", wantFound: true},
		{path: "success", want: "true", wantFound: true},
		{path: "data.total", want: "2", wantFound: true},
		{path: "data.users.0.email", want: `"ada@example.com"`, wantFound: true},
		{path: "data.users.1.id", want: "2", wantFound: true},
		{path: "data.users.0.tags.1", want: `"b"`, wantFound: true},
		{path: "data.nested.deep.value", want: `"found"`, wantFound: true},
		// A negative index counts from the end, so a test can assert on the last
		// element without hardcoding the length.
		{path: "data.users.-1.id", want: "2", wantFound: true},
		// An object key that happens to be numeric must resolve as a key.
		{path: "totals.2024", want: "10", wantFound: true},
		{path: "missing", wantFound: false},
		{path: "data.missing.deep", wantFound: false},
		{path: "data.users.9", wantFound: false},
		{path: "data.users.-9", wantFound: false},
		{path: "data.users.notanindex", wantFound: false},
		// Indexing into a scalar must miss rather than panic.
		{path: "success.nope", wantFound: false},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			got, found := lookupPath(document, test.path)

			if found != test.wantFound {
				t.Fatalf("lookupPath(%q) found = %v, want %v", test.path, found, test.wantFound)
			}
			if !test.wantFound || test.path == "" {
				return
			}
			if encoded := mustFormat(got); encoded != test.want {
				t.Errorf("lookupPath(%q) = %s, want %s", test.path, encoded, test.want)
			}
		})
	}
}

// TestJSONEqualComparesNumbersByValue is what stops nearly every numeric
// assertion from needing a cast: a decoded 1 is a json.Number, an expected 1 is
// an int, and they must compare equal.
func TestJSONEqualComparesNumbersByValue(t *testing.T) {
	tests := []struct {
		name string
		got  any
		want any
		same bool
	}{
		{name: "number vs int", got: json.Number("1"), want: 1, same: true},
		{name: "number vs float", got: json.Number("1"), want: 1.0, same: true},
		{name: "trailing zero", got: json.Number("1.0"), want: 1, same: true},
		{name: "int64", got: json.Number("42"), want: int64(42), same: true},
		{name: "different numbers", got: json.Number("1"), want: 2, same: false},
		{name: "number vs string", got: json.Number("1"), want: "1", same: false},
		{name: "bool", got: true, want: true, same: true},
		{name: "bool mismatch", got: true, want: false, same: false},
		{name: "string", got: "a", want: "a", same: true},
		{name: "null", got: nil, want: nil, same: true},
		{name: "null vs empty string", got: nil, want: "", same: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jsonEqual(test.got, test.want); got != test.same {
				t.Errorf("jsonEqual(%v, %v) = %v, want %v", test.got, test.want, got, test.same)
			}
		})
	}
}

func TestJSONEqualIsStrictAboutStructure(t *testing.T) {
	// Unlike the subset matcher, Equals must reject an extra key.
	got := decode(t, `{"a": 1, "b": 2}`)
	want := decode(t, `{"a": 1}`)

	if jsonEqual(got, want) {
		t.Error("jsonEqual treated a superset as equal; that is what jsonSubset is for")
	}

	if !jsonEqual(decode(t, `[1,2,3]`), decode(t, `[1,2,3]`)) {
		t.Error("jsonEqual reported identical arrays as different")
	}
	if jsonEqual(decode(t, `[1,2]`), decode(t, `[1,2,3]`)) {
		t.Error("jsonEqual treated arrays of different lengths as equal")
	}
}

func TestJSONSubset(t *testing.T) {
	document := decode(t, `{
		"success": true,
		"message": "created",
		"data": {"id": "u1", "name": "Ada", "roles": ["admin", "editor"]}
	}`)

	t.Run("matching subsets", func(t *testing.T) {
		cases := []string{
			`{}`,
			`{"success": true}`,
			`{"data": {"name": "Ada"}}`,
			`{"success": true, "data": {"id": "u1", "name": "Ada"}}`,
			// A prefix of an array is enough, so asserting the first item of a page
			// does not pin the rest.
			`{"data": {"roles": ["admin"]}}`,
			`{"data": {"roles": ["admin", "editor"]}}`,
		}
		for _, want := range cases {
			if diff := jsonSubset(document, decode(t, want), ""); diff != "" {
				t.Errorf("jsonSubset with %s reported: %s", want, diff)
			}
		}
	})

	t.Run("mismatches name the path", func(t *testing.T) {
		cases := []struct {
			want     string
			inReport string
		}{
			{want: `{"success": false}`, inReport: "success"},
			{want: `{"missing": 1}`, inReport: "missing key"},
			{want: `{"data": {"name": "Grace"}}`, inReport: "data.name"},
			{want: `{"data": {"roles": ["editor"]}}`, inReport: "data.roles.0"},
			{want: `{"data": {"roles": ["a","b","c"]}}`, inReport: "at least 3"},
			{want: `{"data": "not an object"}`, inReport: "data"},
			{want: `{"success": {"nested": 1}}`, inReport: "expected an object"},
		}

		for _, test := range cases {
			diff := jsonSubset(document, decode(t, test.want), "")
			if diff == "" {
				t.Errorf("jsonSubset with %s reported no difference", test.want)
				continue
			}
			// A diff that does not say *where* the mismatch is forces the reader to
			// eyeball two JSON blobs.
			if !strings.Contains(diff, test.inReport) {
				t.Errorf("jsonSubset with %s reported %q, want it to mention %q",
					test.want, diff, test.inReport)
			}
		}
	})
}

func TestNormalizeJSON(t *testing.T) {
	t.Run("object literal string is parsed", func(t *testing.T) {
		got, err := normalizeJSON(`{"a": 1}`)
		if err != nil {
			t.Fatalf("normalizeJSON returned %v", err)
		}
		if _, ok := got.(map[string]any); !ok {
			t.Errorf("normalizeJSON parsed a JSON object literal into %T, want a map", got)
		}
	})

	t.Run("array literal string is parsed", func(t *testing.T) {
		got, _ := normalizeJSON(`[1, 2]`)
		if _, ok := got.([]any); !ok {
			t.Errorf("normalizeJSON parsed a JSON array literal into %T, want a slice", got)
		}
	})

	t.Run("plain string stays a string", func(t *testing.T) {
		// The subtle case: ExpectJSONPath("data.name", "Ada") must compare against
		// the string "Ada", and a bare `true` or `12` is far likelier to be an
		// expected scalar than a JSON document.
		for _, in := range []string{"Ada", "true", "12", "null", "", "ada@example.com"} {
			got, err := normalizeJSON(in)
			if err != nil {
				t.Fatalf("normalizeJSON(%q) returned %v", in, err)
			}
			if got != in {
				t.Errorf("normalizeJSON(%q) = %#v, want the plain string", in, got)
			}
		}
	})

	t.Run("struct is marshalled then decoded", func(t *testing.T) {
		type payload struct {
			Name string `json:"name"`
			Age  int    `json:"age"`
		}
		got, err := normalizeJSON(payload{Name: "Ada", Age: 36})
		if err != nil {
			t.Fatalf("normalizeJSON returned %v", err)
		}
		if diff := jsonSubset(got, decode(t, `{"name":"Ada","age":36}`), ""); diff != "" {
			t.Errorf("normalizeJSON of a struct: %s", diff)
		}
	})

	t.Run("invalid JSON bytes error", func(t *testing.T) {
		if _, err := normalizeJSON([]byte(`{"a":`)); err == nil {
			t.Error("normalizeJSON accepted malformed JSON bytes")
		}
	})
}

// TestLargeIntegersSurviveDecoding is why the decoder uses UseNumber: plain
// decoding into any turns an int64 id into a float64, and 9007199254740993
// stops comparing equal to itself.
func TestLargeIntegersSurviveDecoding(t *testing.T) {
	const bigID = "9007199254740993"

	document := decode(t, `{"id": `+bigID+`}`)
	value, found := lookupPath(document, "id")
	if !found {
		t.Fatal("id not found")
	}

	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("id decoded as %T, want json.Number", value)
	}
	if number.String() != bigID {
		t.Errorf("id = %s, want %s — precision was lost", number, bigID)
	}
}

func TestJSONLen(t *testing.T) {
	tests := []struct {
		raw    string
		want   int
		wantOK bool
	}{
		{raw: `[1,2,3]`, want: 3, wantOK: true},
		{raw: `[]`, want: 0, wantOK: true},
		{raw: `{"a":1,"b":2}`, want: 2, wantOK: true},
		{raw: `"abcd"`, want: 4, wantOK: true},
		{raw: `12`, wantOK: false},
		{raw: `true`, wantOK: false},
		{raw: `null`, wantOK: false},
	}

	for _, test := range tests {
		got, ok := jsonLen(decode(t, test.raw))
		if ok != test.wantOK {
			t.Errorf("jsonLen(%s) ok = %v, want %v", test.raw, ok, test.wantOK)
			continue
		}
		if ok && got != test.want {
			t.Errorf("jsonLen(%s) = %d, want %d", test.raw, got, test.want)
		}
	}
}

func TestGoldenPathCannotEscapeTestdata(t *testing.T) {
	// A snapshot name reaches this function, and in update mode it becomes a
	// file write. A traversing name must not be able to overwrite a repo file.
	traversals := []string{
		"../../.github/workflows/ci",
		"../../../etc/passwd",
		"a/b/c",
		"..",
		"",
		"name with spaces",
	}

	for _, name := range traversals {
		path := goldenPath(name)

		if !strings.HasPrefix(path, goldenDir) {
			t.Errorf("goldenPath(%q) = %q, which escapes %q", name, path, goldenDir)
		}
		if strings.Contains(path, "..") {
			t.Errorf("goldenPath(%q) = %q, which still contains a traversal", name, path)
		}
	}
}

func TestIndentJSONSortsKeys(t *testing.T) {
	// Snapshot stability depends on this: Go map iteration order would otherwise
	// make the same response produce a different snapshot on every run.
	first, err := indentJSON([]byte(`{"b":1,"a":2,"c":3}`))
	if err != nil {
		t.Fatalf("indentJSON returned %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := indentJSON([]byte(`{"c":3,"a":2,"b":1}`))
		if err != nil {
			t.Fatalf("indentJSON returned %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("indentJSON is not stable:\n  %s\n  %s", first, again)
		}
	}
}
