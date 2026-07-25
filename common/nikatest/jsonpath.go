package nikatest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// normalizeJSON converts an expected value into the same shape a decoded
// response has, so comparisons never fail merely because one side is an `int`
// and the other a `json.Number`.
//
// A string is treated as JSON when it parses as JSON, and as a plain string
// otherwise — which is what lets both of these read naturally:
//
//	ExpectJSON(`{"success":true}`)
//	ExpectJSONPath("data.name", "Ada")
func normalizeJSON(value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return decodeJSONValue(v)
	case []byte:
		return decodeJSONValue(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if looksLikeJSON(trimmed) {
			if decoded, err := decodeJSONValue([]byte(trimmed)); err == nil {
				return decoded, nil
			}
		}
		return v, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return decodeJSONValue(encoded)
	}
}

// looksLikeJSON avoids treating an ordinary string as a JSON document. Only
// object and array literals qualify: a bare `true` or `12` is far more likely to
// be an expected scalar than a JSON document, and treating it as a document
// would make ExpectJSONPath("count", "12") pass against the number 12.
func looksLikeJSON(s string) bool {
	if s == "" {
		return false
	}
	return s[0] == '{' || s[0] == '['
}

func decodeJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()

	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// lookupPath resolves a dotted path against a decoded JSON value.
//
// Segments index into objects by key and into arrays by number:
//
//	"data.users.0.email"
//
// A numeric segment is tried as an object key first, so a legitimate object key
// like "2024" in `{"totals":{"2024":10}}` still resolves.
func lookupPath(value any, path string) (any, bool) {
	if path == "" {
		return value, true
	}

	current := value
	for _, segment := range strings.Split(path, ".") {
		switch container := current.(type) {
		case map[string]any:
			next, ok := container[segment]
			if !ok {
				return nil, false
			}
			current = next

		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil {
				return nil, false
			}
			// A negative index counts from the end, so a test can assert on the
			// last element without knowing the length.
			if index < 0 {
				index += len(container)
			}
			if index < 0 || index >= len(container) {
				return nil, false
			}
			current = container[index]

		default:
			return nil, false
		}
	}
	return current, true
}

// jsonEqual compares two decoded JSON values structurally.
//
// Numbers are compared by their decimal value rather than by their Go type, so
// an expected `1` matches a decoded `1`, `1.0` and json.Number("1") alike —
// without which nearly every numeric assertion would need a cast.
func jsonEqual(got, want any) bool {
	if gotNum, wantNum, ok := asNumbers(got, want); ok {
		return gotNum == wantNum
	}

	switch wantValue := want.(type) {
	case map[string]any:
		gotMap, ok := got.(map[string]any)
		if !ok || len(gotMap) != len(wantValue) {
			return false
		}
		for key, wantItem := range wantValue {
			gotItem, exists := gotMap[key]
			if !exists || !jsonEqual(gotItem, wantItem) {
				return false
			}
		}
		return true

	case []any:
		gotSlice, ok := got.([]any)
		if !ok || len(gotSlice) != len(wantValue) {
			return false
		}
		for i := range wantValue {
			if !jsonEqual(gotSlice[i], wantValue[i]) {
				return false
			}
		}
		return true

	default:
		return got == want
	}
}

// asNumbers reports whether both values are numeric, and their float values.
func asNumbers(a, b any) (float64, float64, bool) {
	aNum, aOK := toFloat(a)
	bNum, bOK := toFloat(b)
	return aNum, bNum, aOK && bOK
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

// jsonSubset checks that every key and element in want appears in got with an
// equal value, returning a human-readable description of the first difference
// and "" when want is fully contained.
//
// Arrays are compared element-wise and must be at least as long as want, so
// asserting the first two items of a page does not require pinning the rest.
func jsonSubset(got, want any, path string) string {
	switch wantValue := want.(type) {
	case map[string]any:
		gotMap, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("at %s: expected an object, got %s", pathOrRoot(path), typeName(got))
		}
		for key, wantItem := range wantValue {
			gotItem, exists := gotMap[key]
			if !exists {
				return fmt.Sprintf("at %s: missing key %q", pathOrRoot(path), key)
			}
			if diff := jsonSubset(gotItem, wantItem, join(path, key)); diff != "" {
				return diff
			}
		}
		return ""

	case []any:
		gotSlice, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("at %s: expected an array, got %s", pathOrRoot(path), typeName(got))
		}
		if len(gotSlice) < len(wantValue) {
			return fmt.Sprintf("at %s: expected at least %d items, got %d",
				pathOrRoot(path), len(wantValue), len(gotSlice))
		}
		for i := range wantValue {
			if diff := jsonSubset(gotSlice[i], wantValue[i], join(path, strconv.Itoa(i))); diff != "" {
				return diff
			}
		}
		return ""

	default:
		if !jsonEqual(got, want) {
			return fmt.Sprintf("at %s: want %s, got %s",
				pathOrRoot(path), mustFormat(want), mustFormat(got))
		}
		return ""
	}
}

func join(path, segment string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}

func pathOrRoot(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, int:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// jsonLen returns the length of an array, object or string.
func jsonLen(value any) (int, bool) {
	switch v := value.(type) {
	case []any:
		return len(v), true
	case map[string]any:
		return len(v), true
	case string:
		return len(v), true
	default:
		return 0, false
	}
}

// mustFormat renders a value compactly for a failure message.
func mustFormat(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}
