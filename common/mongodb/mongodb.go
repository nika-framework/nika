package mongodb

import (
	"context"
	"fmt"
	"time"

	"github.com/nika-framework/nika"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// MongoDB wraps a *mongo.Client and remembers the default database name.
type MongoDB struct {
	Client   *mongo.Client
	database string
}

// Config holds the parameters for connecting to a MongoDB deployment.
type Config struct {
	URI              string         `json:"uri"`
	Database         string         `json:"database"`
	MaxPoolSize      *uint64        `json:"maxPoolSize"`
	MinPoolSize      *uint64        `json:"minPoolSize"`
	SocketTimeout    *time.Duration `json:"socketTimeout"`
	ConnectTimeout   *time.Duration `json:"connectTimeout"`
	ServerSelection  *time.Duration `json:"serverSelectionTimeout"`
	MaxConnIdleTime  *time.Duration `json:"maxConnIdleTime"`
	RetryWrites      *bool          `json:"retryWrites"`
}

// Setup connects to MongoDB, pings the primary, and registers the resulting
// client and default database as singletons.
func Setup(app *nika.App, cfg Config) (*MongoDB, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("mongodb: URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("mongodb: Database is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(cfg.URI)

	if cfg.MaxPoolSize != nil {
		clientOptions.SetMaxPoolSize(*cfg.MaxPoolSize)
	} else {
		clientOptions.SetMaxPoolSize(100)
	}

	if cfg.MinPoolSize != nil {
		clientOptions.SetMinPoolSize(*cfg.MinPoolSize)
	}

	if cfg.SocketTimeout != nil {
		clientOptions.SetSocketTimeout(*cfg.SocketTimeout)
	}
	if cfg.ConnectTimeout != nil {
		clientOptions.SetConnectTimeout(*cfg.ConnectTimeout)
	}
	if cfg.ServerSelection != nil {
		clientOptions.SetServerSelectionTimeout(*cfg.ServerSelection)
	} else {
		clientOptions.SetServerSelectionTimeout(5 * time.Second)
	}
	if cfg.MaxConnIdleTime != nil {
		clientOptions.SetMaxConnIdleTime(*cfg.MaxConnIdleTime)
	}
	if cfg.RetryWrites != nil {
		clientOptions.SetRetryWrites(*cfg.RetryWrites)
	}

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("mongodb connect error: %w", err)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
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

// Database returns the named Mongo database, or the configured default when
// name is empty.
func (m *MongoDB) Database(name string) *mongo.Database {
	if name == "" {
		name = m.database
	}
	return m.Client.Database(name)
}

// DefaultDatabase returns the database configured at Setup.
func (m *MongoDB) DefaultDatabase() *mongo.Database {
	return m.Client.Database(m.database)
}

// DatabaseName returns the default database name.
func (m *MongoDB) DatabaseName() string {
	return m.database
}

// Collection returns a collection in the given database.
func (m *MongoDB) Collection(database, collection string) *mongo.Collection {
	if database == "" {
		database = m.database
	}
	return m.Client.Database(database).Collection(collection)
}

// Close cleanly disconnects the underlying client.
func (m *MongoDB) Close(ctx context.Context) error {
	return m.Client.Disconnect(ctx)
}

// HealthCheck pings the primary.
func (m *MongoDB) HealthCheck(ctx context.Context) error {
	return m.Client.Ping(ctx, readpref.Primary())
}
