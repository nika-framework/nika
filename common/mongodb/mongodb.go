package mongodb

import (
	"context"
	"fmt"
	"time"

	"github.com/nika-framework/nika"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoDB struct {
	Client *mongo.Client
	database string
}

type Config struct {
	URI      string
	Database string
	MaxPoolSize  *uint64          `json:"maxPoolSize"`  // Maximum number of connections in the connection pool
    MinPoolSize  *uint64          `json:"minPoolSize"`  // Minimum number of connections in the connection pool
    SocketTimeout *time.Duration `json:"socketTimeout"` // Timeout for socket operations
}

func Setup(app *nika.App, cfg Config) (*MongoDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    clientOptions := options.Client().ApplyURI(cfg.URI)

    if cfg.MaxPoolSize != nil {
        clientOptions.SetMaxPoolSize(*cfg.MaxPoolSize)
    }
    
    if cfg.MinPoolSize != nil {
        clientOptions.SetMinPoolSize(*cfg.MinPoolSize)
    }
    
    if cfg.SocketTimeout != nil {
        clientOptions.SetSocketTimeout(*cfg.SocketTimeout)
    }

    client, err := mongo.Connect(ctx, clientOptions)
    if err != nil {
        return nil, fmt.Errorf("mongodb connect error: %w", err)
    }

    if err := client.Ping(ctx, nil); err != nil {
        return nil, fmt.Errorf("mongodb ping error: %w", err)
    }

    db := &MongoDB{
        Client:   client,
        database: cfg.Database,
    }

    app.RegisterSingleton(db)
    app.RegisterSingleton(client.Database(cfg.Database))

    fmt.Println("✅ MongoDB connected")
    return db, nil
}

func (m *MongoDB) Database(name string) *mongo.Database {
	return m.Client.Database(name)
}

func (m *MongoDB) Collection(database, collection string) *mongo.Collection {
	return m.Client.Database(database).Collection(collection)
}