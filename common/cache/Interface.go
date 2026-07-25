package cache

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get and TTL when a key is absent or has expired.
//
// Every provider maps its driver-specific "missing" signal onto this single
// error (redis.Nil, os.ErrNotExist, a map miss) so callers can write
// errors.Is(err, cache.ErrNotFound) without knowing which driver they got.
var ErrNotFound = errors.New("cache: key not found")

// ErrKeyEmpty is returned when a caller passes an empty key. An empty key is
// always a bug, and for the file provider it would name the cache directory
// itself rather than an entry inside it.
var ErrKeyEmpty = errors.New("cache: key must not be empty")

// NoExpiry is the expiration value meaning "store without an expiry".
const NoExpiry = time.Duration(0)

// Provider is the cache contract. Implementations must be safe for concurrent
// use by multiple goroutines.
type Provider interface {
	// Set stores value under key, overwriting any existing entry. An expiration
	// of 0 stores the entry without an expiry. Values that are not string,
	// []byte or an encoding.BinaryMarshaler are JSON-encoded.
	Set(ctx context.Context, key string, value any, expiration time.Duration) error

	// SetNX stores value only if key is absent (or present but already expired),
	// and reports whether it stored.
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) (bool, error)

	// Get returns the stored value, or ("", ErrNotFound) when key is absent or
	// expired.
	Get(ctx context.Context, key string) (string, error)

	// Delete removes key. Deleting an absent key is not an error.
	Delete(ctx context.Context, key string) error

	// Increment atomically adds delta to the integer stored at key, treating an
	// absent key as 0, and returns the new value. Rate limiting is built on this,
	// so it must be atomic with respect to concurrent callers.
	Increment(ctx context.Context, key string, delta int64) (int64, error)

	// TTL returns the remaining lifetime of key: a positive duration for an entry
	// with an expiry, 0 for an entry stored without one, and ErrNotFound when the
	// key is absent or expired.
	TTL(ctx context.Context, key string) (time.Duration, error)

	Close() error
	Ping(ctx context.Context) error
}
