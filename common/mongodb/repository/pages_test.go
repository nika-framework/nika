package repository

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestContainsStage(t *testing.T) {
	cases := []struct {
		name     string
		pipeline []any
		op       string
		want     bool
	}{
		{"empty pipeline", nil, "$sort", false},
		{"bson.M match", []any{bson.M{"$sort": bson.M{"_id": 1}}}, "$sort", true},
		{"bson.M miss", []any{bson.M{"$match": bson.M{"a": 1}}}, "$sort", false},
		{"plain map match", []any{map[string]any{"$sort": 1}}, "$sort", true},
		{"plain map miss", []any{map[string]any{"$limit": 1}}, "$sort", false},
		{"bson.D match", []any{bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}}}, "$sort", true},
		{"bson.D miss", []any{bson.D{{Key: "$match", Value: bson.D{}}}}, "$sort", false},
		{
			"later stage matches",
			[]any{bson.M{"$match": bson.M{"a": 1}}, bson.D{{Key: "$sort", Value: 1}}},
			"$sort",
			true,
		},
		{
			"mixed representations",
			[]any{map[string]any{"$match": 1}, bson.M{"$project": 1}, bson.D{{Key: "$sort", Value: 1}}},
			"$sort",
			true,
		},
		{"different operator", []any{bson.M{"$sort": 1}}, "$match", false},
		// A stage of an unrecognised type must not be reported as a match.
		{"unknown stage type", []any{"$sort"}, "$sort", false},
		{"nil stage", []any{nil}, "$sort", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsStage(tc.pipeline, tc.op); got != tc.want {
				t.Errorf("containsStage(%v, %q) = %v, want %v", tc.pipeline, tc.op, got, tc.want)
			}
		})
	}
}

func TestClampPageSize(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, DefaultPageSize},
		{-1, DefaultPageSize},
		{-1000, DefaultPageSize},
		{1, 1},
		{15, 15},
		{MaxPageSize, MaxPageSize},
		// A perPage lifted from a query string must not be able to ask the server
		// to buffer the whole collection.
		{MaxPageSize + 1, MaxPageSize},
		{100000000, MaxPageSize},
		{1 << 40, MaxPageSize},
	}

	for _, tc := range cases {
		if got := ClampPageSize(tc.in); got != tc.want {
			t.Errorf("ClampPageSize(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestHasLimit(t *testing.T) {
	if hasLimit(nil) {
		t.Error("hasLimit(nil) = true, want false")
	}
	if hasLimit([]*options.FindOptions{nil}) {
		t.Error("hasLimit([nil]) = true, want false")
	}
	if hasLimit([]*options.FindOptions{options.Find()}) {
		t.Error("hasLimit(no limit set) = true, want false")
	}
	if !hasLimit([]*options.FindOptions{options.Find().SetLimit(5)}) {
		t.Error("hasLimit(limit set) = false, want true")
	}
	// A limit anywhere in the list counts, since the driver merges them in order.
	if !hasLimit([]*options.FindOptions{options.Find().SetSort(bson.M{"_id": 1}), options.Find().SetLimit(5)}) {
		t.Error("hasLimit(limit in a later option set) = false, want true")
	}
}
