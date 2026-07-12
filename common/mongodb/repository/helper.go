package repository

import (
	"fmt"
	"reflect"
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

func ToLikeRegex(query string) bson.M {
	return bson.M{
		"$regex":   query,
		"$options": "i",
	}
}

func GetSafeString(m map[string]any, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}
func GetSafeDate(m map[string]any, key string) time.Time {
	if val, ok := m[key].(time.Time); ok {
		return val
	}
	return time.Now().UTC()
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
	switch fv.Kind() {
	case reflect.Interface:
		fv.Set(reflect.ValueOf(insertedID))
	case reflect.String:
		if oid, ok := insertedID.(primitive.ObjectID); ok {
			fv.SetString(oid.Hex())
		}
	default:
		rv := reflect.ValueOf(insertedID)
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
