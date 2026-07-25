package mongodb

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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
	URI             string         `json:"uri"`
	Database        string         `json:"database"`
	MaxPoolSize     *uint64        `json:"maxPoolSize"`
	MinPoolSize     *uint64        `json:"minPoolSize"`
	SocketTimeout   *time.Duration `json:"socketTimeout"`
	ConnectTimeout  *time.Duration `json:"connectTimeout"`
	ServerSelection *time.Duration `json:"serverSelectionTimeout"`
	MaxConnIdleTime *time.Duration `json:"maxConnIdleTime"`
	RetryWrites     *bool          `json:"retryWrites"`
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

	warnInsecureURI(cfg.URI)

	// Passing a ctx that this function's `defer cancel()` will cancel is safe on
	// the v1 driver: mongo.Connect calls NewClient followed by Client.Connect,
	// and Client.Connect only uses ctx for the optional field-level-encryption
	// sub-clients — the topology is started through connector.Connect(), which
	// takes no context and keeps none. Cancelling afterwards therefore does not
	// tear down the pool. (Verified against mongo-driver v1.17.x mongo/client.go.)
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

	if app != nil {
		app.RegisterSingleton(db)
		app.RegisterSingleton(client.Database(cfg.Database))

		// Disconnect drains checked-out connections and ends open sessions;
		// without this the client was simply abandoned at process exit.
		app.OnShutdown(func(shutdownCtx context.Context) error {
			return client.Disconnect(shutdownCtx)
		})
	}

	fmt.Println("✅ MongoDB connected")
	return db, nil
}

// warnInsecureURI logs when the connection string carries no credentials and is
// not pointed at a local instance.
//
// An unauthenticated MongoDB reachable over the network is the single most
// common cause of public data leaks in this ecosystem, and the driver connects to
// one without complaint. TLS is not enforced here — a private-network deployment
// can be a legitimate choice — but a remote target with no auth is worth saying
// out loud at startup.
func warnInsecureURI(uri string) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return
	}

	hasCredentials := parsed.User != nil && parsed.User.Username() != ""
	if !hasCredentials && strings.Contains(uri, "authMechanism") {
		// X.509 / AWS IAM auth carry no username in the URI.
		hasCredentials = true
	}
	if hasCredentials {
		if !strings.EqualFold(parsed.Scheme, "mongodb+srv") &&
			!strings.Contains(uri, "tls=true") && !strings.Contains(uri, "ssl=true") &&
			!isLocalHost(parsed.Host) {
			fmt.Println("⚠️  MongoDB: connecting to a remote host without TLS — credentials cross the network in the clear")
		}
		return
	}

	if isLocalHost(parsed.Host) {
		return
	}
	fmt.Printf("⚠️  MongoDB: URI for %q has no credentials — the deployment appears to accept unauthenticated access\n", parsed.Host)
}

func isLocalHost(host string) bool {
	if host == "" {
		return false
	}
	// Strip the port; a URI may list several hosts, so check them all.
	for _, h := range strings.Split(host, ",") {
		name := h
		if idx := strings.LastIndexByte(name, ':'); idx >= 0 && !strings.Contains(name, "]") {
			name = name[:idx]
		}
		name = strings.Trim(name, "[]")
		switch strings.ToLower(name) {
		case "localhost", "127.0.0.1", "::1":
			continue
		default:
			return false
		}
	}
	return true
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
