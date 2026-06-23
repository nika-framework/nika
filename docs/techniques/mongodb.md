# Mongo (MongoDB)

Nika provides a complete MongoDB integration through the `common/mongodb` package, including connection management and a **generic repository pattern** for type-safe CRUD operations.

## Installation

```bash
go get github.com/sajadweb/nika
# MongoDB driver is included as a dependency
```

## Setup

### Connection

```go
package main

import (
    "github.com/sajadweb/nika"
    "github.com/sajadweb/nika/common/mongodb"
)

func main() {
    app := nika.NewApp()

    // Connect to MongoDB and register in DI container
    _, err := mongodb.Setup(app, mongodb.Config{
        URI:      "mongodb://localhost:27017",
        Database: "myapp",
    })
    if err != nil {
        panic(err)
    }

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

### Configuration with `.env`

```go
cfg := config.Setup(app, "")

db, err := mongodb.Setup(app, mongodb.Config{
    URI:      cfg.Get("MONGO_URI", "mongodb://localhost:27017"),
    Database: cfg.Get("MONGO_DATABASE", "myapp"),
})
```

### What Gets Registered

`mongodb.Setup()` registers two types in the DI container:

| Registered Type | How to Inject |
|----------------|---------------|
| `*mongodb.MongoDB` | The Nika MongoDB wrapper |
| `*mongo.Database` | The native MongoDB driver database |

```go
// Inject the Nika wrapper
func NewUserService(db *mongodb.MongoDB) *UserService { ... }

// Or inject the native driver directly
func NewUserService(db *mongo.Database) *UserService { ... }
```

## Generic Repository Pattern

The `common/mongodb/repository` package provides a **generic base repository** with type-safe CRUD operations.

### Define Your Model

```go
package models

import "go.mongodb.org/mongo-driver/bson/primitive"

type User struct {
    ID       primitive.ObjectID `json:"id" bson:"_id"`
    Name     string            `json:"name" bson:"name"`
    Email    string            `json:"email" bson:"email"`
    Age      int               `json:"age" bson:"age"`
    Role     string            `json:"role" bson:"role"`
    IsActive bool              `json:"is_active" bson:"is_active"`
}
```

### Create a Repository

```go
package src

import (
    "context"
    "github.com/sajadweb/nika/common/mongodb"
    "github.com/sajadweb/nika/common/mongodb/repository"
    "go.mongodb.org/mongo-driver/mongo"
)

type UserRepository struct {
    repo *repository.BaseRepository[models.User]
}

func NewUserRepository(db *mongodb.MongoDB) *UserRepository {
    collection := db.Collection(db.Database(), "users")
    return &UserRepository{
        repo: repository.NewBaseRepository[models.User](collection),
    }
}
```

### Available Methods

#### Create Operations
{% raw %}
```go
// Create a single document
user := &models.User{Name: "Alice", Email: "alice@example.com"}
created, err := userRepo.repo.Create(ctx, user)

// Create or update (upsert)
err := userRepo.repo.CreateAndUpdate(ctx, user)

// Save one (insert)
err := userRepo.repo.SaveOne(ctx, user)

```
{% endraw %}

#### Read Operations

```go
// Find one by ID
id, _ := primitive.ObjectIDFromHex("507f1f77bcf86cd799439011")
user, err := userRepo.repo.FindOneByID(ctx, id)

// Find one by filter
user, err := userRepo.repo.FindOne(ctx, bson.M{"email": "alice@example.com"})

// Find by condition
users, err := userRepo.repo.FindByCondition(ctx, bson.M{"role": "admin"})

// Find all
users, err := userRepo.repo.FindAll(ctx, bson.M{})

// Check existence
exists, err := userRepo.repo.ExistsByID(ctx, id)
exists, err := userRepo.repo.ExistsByCondition(ctx, bson.M{"email": "alice@example.com"})

// Count
count, err := userRepo.repo.CountByCondition(ctx, bson.M{"role": "admin"})

// Find with aggregate pipeline
results, err := userRepo.repo.FindWithAggregate(ctx, []any{
    bson.M{"$match": bson.M{"is_active": true}},
    bson.M{"$group": bson.M{"_id": "$role", "count": bson.M{"$sum": 1}}},
})

// Find with relations (populate)
users, err := userRepo.repo.FindWithRelations(ctx, bson.M{"_id": id})

// Pagination
result, err := userRepo.repo.Pages(ctx, pipeline, page, perPage)
// result.Data  → []map[string]any
// result.Total → int64
```

#### Update Operations

```go
// Update by ID
err := userRepo.repo.UpdateOneByID(ctx, id, bson.M{"$set": bson.M{"name": "Updated"}})

// Update by condition
err := userRepo.repo.UpdateOne(ctx, bson.M{"email": "alice@example.com"}, bson.M{"$set": bson.M{"name": "Alice Updated"}})

// Update and return updated document
updated, err := userRepo.repo.UpdateAndFindOne(ctx, bson.M{"email": "old@example.com"}, bson.M{"$set": bson.M{"email": "new@example.com"}})

// Update many
err := userRepo.repo.UpdateMany(ctx, bson.M{"role": "user"}, bson.M{"$set": bson.M{"is_active": true}})

// Increment
err := userRepo.repo.Increment(ctx, bson.M{"_id": id}, "login_count", 1)

// Decrement
err := userRepo.repo.Decrement(ctx, bson.M{"_id": id}, "credits", 10)
```

#### Delete Operations

```go
// Delete by ID
err := userRepo.repo.DeleteByID(ctx, id)

// Delete by condition
err := userRepo.repo.DeleteMany(ctx, bson.M{"is_active": false})

// Delete one
err := userRepo.repo.DeleteOne(ctx, bson.M{"email": "old@example.com"})
```

## Helper Functions

```go
import "github.com/sajadweb/nika/common/mongodb/repository"

// Parse string to ObjectID
id, err := repository.ParseObjectID("507f1f77bcf86cd799439011")

// Create regex filter for LIKE queries
filter := repository.ToLikeRegex("alice") // → bson.M{"$regex": "alice", "$options": "i"}

// Get safe string from map
name := repository.GetSafeString(data, "name")

// Get safe date from map
date := repository.GetSafeDate(data, "created_at")
```

## Pagination

The `Pages` method uses MongoDB's `$facet` aggregation for efficient pagination:

```go
func (ctrl *UserController) List(c *gin.Context) {
    page, _ := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 64)
    perPage, _ := strconv.ParseInt(c.DefaultQuery("per_page", "10"), 10, 64)

    pipeline := []any{
        bson.M{"$match": bson.M{"is_active": true}},
    }

    result, err := ctrl.userRepo.repo.Pages(c, pipeline, page, perPage)
    if err != nil {
        response.JSONError(c, 500, "DB_ERROR", err.Error())
        return
    }

    response.Ok(c, gin.H{
        "data":  result.Data,
        "total": result.Total,
    })
}
```

## MongoDB Helper Methods

```go
// Access the underlying client
db := &mongodb.MongoDB{Client: client, database: "myapp"}

// Get a specific database
myDB := db.Database("analytics")

// Get a specific collection
usersCol := db.Collection("myapp", "users")
```
