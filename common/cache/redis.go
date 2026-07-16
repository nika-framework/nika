package cache

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

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
		return nil, err
	}

	return &RedisProvider{
		client: rdb,
	}, nil
}

func (r *RedisProvider) Set(ctx context.Context, key string, value any, exp time.Duration) error {
	return r.client.Set(ctx, key, value, exp).Err()
}

func (r *RedisProvider) SetNX(ctx context.Context, key string, value any, exp time.Duration) (bool, error) {
	return r.client.SetNX(ctx, key, value, exp).Result()
}

func (r *RedisProvider) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

func (r *RedisProvider) Delete(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

func (r *RedisProvider) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisProvider) Close() error {
	return r.client.Close()
}
