package cache

import (
	"fmt"

	"github.com/nika-framework/nika"
)

type Cache struct {
	Provider Provider
}

type Config struct {
	Driver string // redis,file,memcached
	URL string
}

func Setup(app *nika.App, cfg Config) (*Cache, error) {

	var provider Provider
	var err error

	switch cfg.Driver {

	case "redis":
		provider, err = NewRedisProvider(cfg.URL)

	case "file":
		provider = NewFileProvider(cfg.URL)

	case "memcached":
		return nil, fmt.Errorf("memcached provider not implemented")

	default:
		return nil, fmt.Errorf("unknown cache driver: %s", cfg.Driver)
	}

	if err != nil {
		return nil, err
	}

	cache := &Cache{
		Provider: provider,
	}

	app.RegisterSingleton(cache)

	return cache, nil
}
