package ratelimit

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	"github.com/ulule/limiter/v3"
	limitgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	"github.com/ulule/limiter/v3/drivers/store/memory"
	redisstore "github.com/ulule/limiter/v3/drivers/store/redis"
)

const (
	DriverMemory = "memory"
	DriverRedis  = "redis"
)

type KeyFunc func(*gin.Context) string

type Config struct {
	Requests        int
	Window          time.Duration
	Driver          string
	Prefix          string
	StatusCode      int
	Message         string
	CleanupInterval time.Duration
	KeyFunc         KeyFunc
	Skip            func(*gin.Context) bool
	Store           limiter.Store
	RedisClient     redisstore.Client
	LimiterOptions  []limiter.Option
}

type RateLimiter struct {
	Limiter *limiter.Limiter
	handler gin.HandlerFunc
}

func Setup(app *nika.App, cfg Config) (*RateLimiter, error) {
	rateLimiter, err := New(cfg)
	if err != nil {
		return nil, err
	}

	app.Use(rateLimiter.Middleware())
	app.RegisterSingleton(rateLimiter)
	app.RegisterSingleton(rateLimiter.Limiter)

	fmt.Println("✅ RateLimit initialized")
	return rateLimiter, nil
}

func New(cfg Config) (*RateLimiter, error) {
	if cfg.Requests <= 0 {
		return nil, fmt.Errorf("ratelimit: requests must be greater than zero")
	}
	if cfg.Window <= 0 {
		return nil, fmt.Errorf("ratelimit: window must be greater than zero")
	}

	store, err := buildStore(cfg)
	if err != nil {
		return nil, err
	}

	rate := limiter.Rate{
		Period: cfg.Window,
		Limit:  int64(cfg.Requests),
	}
	core := limiter.New(store, rate, cfg.LimiterOptions...)

	handler := buildMiddleware(core, cfg)
	return &RateLimiter{
		Limiter: core,
		handler: handler,
	}, nil
}

func (r *RateLimiter) Middleware() gin.HandlerFunc {
	return r.handler
}

func buildStore(cfg Config) (limiter.Store, error) {
	if cfg.Store != nil {
		return cfg.Store, nil
	}

	driver := cfg.Driver
	if driver == "" {
		driver = DriverMemory
	}

	storeOptions := limiter.StoreOptions{
		Prefix:          cfg.Prefix,
		CleanUpInterval: cfg.CleanupInterval,
	}
	if storeOptions.Prefix == "" {
		storeOptions.Prefix = limiter.DefaultPrefix
	}
	if storeOptions.CleanUpInterval <= 0 {
		storeOptions.CleanUpInterval = limiter.DefaultCleanUpInterval
	}

	switch driver {
	case DriverMemory:
		return memory.NewStoreWithOptions(storeOptions), nil
	case DriverRedis:
		if cfg.RedisClient == nil {
			return nil, fmt.Errorf("ratelimit: redis client is required when driver is redis")
		}
		return redisstore.NewStoreWithOptions(cfg.RedisClient, storeOptions)
	default:
		return nil, fmt.Errorf("ratelimit: unknown driver %q", driver)
	}
}

func buildMiddleware(core *limiter.Limiter, cfg Config) gin.HandlerFunc {
	statusCode := cfg.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusTooManyRequests
	}

	message := cfg.Message
	if message == "" {
		message = "rate limit exceeded"
	}

	options := []limitgin.Option{
		limitgin.WithLimitReachedHandler(func(c *gin.Context) {
			c.AbortWithStatusJSON(statusCode, gin.H{
				"success": false,
				"error": gin.H{
					"code":    statusCode,
					"message": "RATE_LIMIT_ERROR",
					"details": []gin.H{
						{
							"field":   "rate_limit",
							"message": message,
						},
					},
				},
			})
		}),
		limitgin.WithErrorHandler(func(c *gin.Context, err error) {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error": gin.H{
					"code":    http.StatusInternalServerError,
					"message": "RATE_LIMIT_ERROR",
					"details": []gin.H{
						{
							"field":   "rate_limit",
							"message": err.Error(),
						},
					},
				},  
			})
		}),
	}

	if cfg.KeyFunc != nil {
		options = append(options, limitgin.WithKeyGetter(limitgin.KeyGetter(cfg.KeyFunc)))
	}

	handler := limitgin.NewMiddleware(core, options...)
	if cfg.Skip == nil {
		return handler
	}

	return func(c *gin.Context) {
		if cfg.Skip(c) {
			c.Next()
			return
		}
		handler(c)
	}
}
