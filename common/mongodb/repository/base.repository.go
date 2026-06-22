package repository

import (
	"go.mongodb.org/mongo-driver/mongo"
)

type BaseRepository[T any] struct {
	Collection *mongo.Collection
}

func NewBaseRepository[T any](
	collection *mongo.Collection,
) *BaseRepository[T] {
	return &BaseRepository[T]{
		Collection: collection,
	}
}