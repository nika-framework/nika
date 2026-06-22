package repository

import (
	"fmt"
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
    if val, ok := m[key].(time.Time ); ok {
        return val
    }
    return time.Now().UTC()
}