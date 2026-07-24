package sqldb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nika-framework/nika"
)

// Driver represents the supported SQL database drivers.
type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverMySQL    Driver = "mysql"
	DriverSQLite   Driver = "sqlite3"
)

// DB wraps the standard sql.DB with additional metadata.
type DB struct {
	Conn   *sql.DB
	driver Driver
	dbName string
}

// Config holds the configuration for establishing a SQL database connection.
type Config struct {
	// Driver specifies the database driver: "postgres", "mysql", "sqlite3"
	Driver Driver `json:"driver"`

	// DSN is the Data Source Name (connection string).
	// Postgres example: "postgres://user:pass@localhost:5432/dbname?sslmode=disable"
	// MySQL example:    "user:pass@tcp(localhost:3306)/dbname?parseTime=true"
	// SQLite example:   "file:test.db?cache=shared&mode=rwc"
	DSN string `json:"dsn"`

	// Database name (used for logging and metadata)
	Database string `json:"database"`

	// MaxOpenConns sets the maximum number of open connections to the database.
	MaxOpenConns int `json:"maxOpenConns"`

	// MaxIdleConns sets the maximum number of idle connections in the pool.
	MaxIdleConns int `json:"maxIdleConns"`

	// ConnMaxLifetime sets the maximum amount of time a connection may be reused.
	ConnMaxLifetime *time.Duration `json:"connMaxLifetime"`

	// ConnMaxIdleTime sets the maximum amount of time a connection may be idle.
	ConnMaxIdleTime *time.Duration `json:"connMaxIdleTime"`
}

// Setup creates a new SQL database connection, pings it, and registers it
// as a singleton in the nika application container.
//
// NOTE: The database driver must be registered by importing its package
// with a blank identifier in main (e.g. `_ "github.com/lib/pq"`).
func Setup(app *nika.App, cfg Config) (*DB, error) {
	if cfg.Driver == "" {
		return nil, fmt.Errorf("sqldb: driver is required")
	}
	if cfg.DSN == "" {
		return nil, fmt.Errorf("sqldb: dsn is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := sql.Open(string(cfg.Driver), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sqldb open error: %w", err)
	}

	// Connection pool configuration with production-safe defaults.
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	conn.SetMaxOpenConns(maxOpen)

	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen / 2
		if maxIdle < 2 {
			maxIdle = 2
		}
	}
	conn.SetMaxIdleConns(maxIdle)

	if cfg.ConnMaxLifetime != nil {
		conn.SetConnMaxLifetime(*cfg.ConnMaxLifetime)
	} else {
		conn.SetConnMaxLifetime(30 * time.Minute)
	}

	if cfg.ConnMaxIdleTime != nil {
		conn.SetConnMaxIdleTime(*cfg.ConnMaxIdleTime)
	} else {
		conn.SetConnMaxIdleTime(5 * time.Minute)
	}

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sqldb ping error: %w", err)
	}

	db := &DB{
		Conn:   conn,
		driver: cfg.Driver,
		dbName: cfg.Database,
	}

	app.RegisterSingleton(db)
	app.RegisterSingleton(conn)

	fmt.Printf("✅ SQL Database connected (%s)\n", cfg.Driver)
	return db, nil
}

// Driver returns the database driver type.
func (d *DB) Driver() Driver {
	return d.driver
}

// DatabaseName returns the database name.
func (d *DB) DatabaseName() string {
	return d.dbName
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.Conn.Close()
}

// BeginTx starts a new database transaction with the given options.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.Conn.BeginTx(ctx, opts)
}

// HealthCheck performs a quick database health check.
func (d *DB) HealthCheck(ctx context.Context) error {
	return d.Conn.PingContext(ctx)
}

// Stats returns the database connection pool statistics.
func (d *DB) Stats() sql.DBStats {
	return d.Conn.Stats()
}
