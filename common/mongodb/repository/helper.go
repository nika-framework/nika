package repository

import (
	"fmt"
	"reflect"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func ParseObjectID(param string) (primitive.ObjectID, error) {
	objID, err := primitive.ObjectIDFromHex(param)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("invalid object id: %w", err)
	}

	return objID, nil
}

// ToLikeRegex builds a case-insensitive "contains" match for a literal search
// term.
//
// The term is escaped with regexp.QuoteMeta, so a user searching for "a.*b" or
// "(a+)+" gets those characters matched literally instead of interpreted. Passing
// a raw search box straight into $regex is both an injection (the pattern
// changes which documents match, e.g. "^admin" enumerating accounts) and a
// denial-of-service vector, because MongoDB evaluates $regex with a backtracking
// engine.
func ToLikeRegex(query string) bson.M {
	return bson.M{
		"$regex":   regexp.QuoteMeta(query),
		"$options": "i",
	}
}

// ToLikeRegexRaw builds a case-insensitive match from a caller-supplied *pattern*
// rather than a literal.
//
// Only for patterns the developer wrote. It validates the pattern against the
// same length and backtracking rules as SanitizeUserFilter and returns an error
// when it fails, but it cannot know whether the pattern came from a request —
// that is the caller's responsibility.
func ToLikeRegexRaw(pattern string) (bson.M, error) {
	if err := ValidateRegexPattern(pattern); err != nil {
		return nil, err
	}
	return bson.M{
		"$regex":   pattern,
		"$options": "i",
	}, nil
}

func GetSafeString(m map[string]any, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// GetSafeDate returns the time.Time at key, or the zero time when the key is
// missing or holds another type.
//
// It used to return time.Now() on a miss, which is the worst possible default: a
// missing created_at silently became "now", so absent data was indistinguishable
// from fresh data and got persisted as if it were real. Callers should check
// IsZero, or use GetSafeDateOr to state a fallback explicitly.
func GetSafeDate(m map[string]any, key string) time.Time {
	if val, ok := m[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}

// GetSafeDateOr returns the time.Time at key, or fallback when it is missing or
// of another type.
func GetSafeDateOr(m map[string]any, key string, fallback time.Time) time.Time {
	if val, ok := m[key].(time.Time); ok {
		return val
	}
	return fallback
}

func GetSafeBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// setInsertedID injects the MongoDB-generated _id into the struct field that is
// tagged with bson:"_id" (or named Id/ObjectID). It mutates the value in-place
// via reflection only once per insert, avoiding the previous marshal/unmarshal
// round-trips. If no suitable field is found, it silently does nothing.
func setInsertedID(data any, insertedID any) {
	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("bson")
		name := field.Name
		if tag != "" {
			name = tag
			if idx := indexByte(tag, ','); idx >= 0 {
				name = tag[:idx]
			}
		}
		if name != "_id" && !isIDName(field.Name) {
			continue
		}
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		assignObjectID(fv, insertedID)
		return
	}
}

func isIDName(name string) bool {
	switch name {
	case "ID", "Id", "ObjectID", "ObjectId":
		return true
	}
	return false
}

func assignObjectID(fv reflect.Value, insertedID any) {
	rv := reflect.ValueOf(insertedID)
	// reflect.ValueOf(nil) is the zero Value and Set on it panics; an interface
	// field also rejects a type that does not implement it.
	if !rv.IsValid() {
		return
	}

	switch fv.Kind() {
	case reflect.Interface:
		if !rv.Type().Implements(fv.Type()) {
			return
		}
		fv.Set(rv)
	case reflect.String:
		if oid, ok := insertedID.(primitive.ObjectID); ok {
			fv.SetString(oid.Hex())
		}
	default:
		if rv.Type().ConvertibleTo(fv.Type()) {
			fv.Set(rv.Convert(fv.Type()))
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
