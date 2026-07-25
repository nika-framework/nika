package cache

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisProvider is the only provider whose SetNX and Increment are correct across
// processes, because Redis executes each as a single server-side command. Use it
// for distributed locks and shared rate-limit counters.
type RedisProvider struct {
	client *goredis.Client
}

func NewRedisProvider(url string) (*RedisProvider, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, err
	}

	rdb := goredis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		// Close the client we just opened; returning without it leaks the
		// connection pool goroutines for the lifetime of the process.
		_ = rdb.Close()
		return nil, err
	}

	return &RedisProvider{
		client: rdb,
	}, nil
}

// Client exposes the underlying client for commands this interface does not cover.
func (r *RedisProvider) Client() *goredis.Client { return r.client }

func (r *RedisProvider) Set(ctx context.Context, key string, value any, exp time.Duration) error {
	if key == "" {
		return ErrKeyEmpty
	}
	// Encode here rather than letting go-redis do it, so the bytes read back are
	// the same whichever provider is configured.
	str, err := marshalValue(value)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, str, exp).Err()
}

func (r *RedisProvider) SetNX(ctx context.Context, key string, value any, exp time.Duration) (bool, error) {
	if key == "" {
		return false, ErrKeyEmpty
	}
	str, err := marshalValue(value)
	if err != nil {
		return false, err
	}
	return r.client.SetNX(ctx, key, str, exp).Result()
}

func (r *RedisProvider) Get(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrKeyEmpty
	}
	value, err := r.client.Get(ctx, key).Result()
	if err != nil {
		// redis.Nil is the driver's "no such key"; translate it so callers can
		// compare against cache.ErrNotFound regardless of driver.
		if errors.Is(err, goredis.Nil) {
			return "", ErrNotFound
		}
		return "", err
	}
	return value, nil
}

func (r *RedisProvider) Delete(ctx context.Context, key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	return r.client.Del(ctx, key).Err()
}

// Increment uses INCRBY, which is atomic server-side, so this is the provider to
// use for a rate-limit counter shared by several instances.
func (r *RedisProvider) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}
	return r.client.IncrBy(ctx, key, delta).Result()
}

// TTL normalises Redis's two sentinel replies: -2 means the key is gone, -1 means
// it exists without an expiry.
func (r *RedisProvider) TTL(ctx context.Context, key string) (time.Duration, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}
	ttl, err := r.client.TTL(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	switch {
	case ttl == -1: // exists, no expiry set
		return NoExpiry, nil
	case ttl < 0: // -2, or any future negative sentinel: treat as absent
		return 0, ErrNotFound
	}
	return ttl, nil
}

func (r *RedisProvider) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisProvider) Close() error {
	return r.client.Close()
}
