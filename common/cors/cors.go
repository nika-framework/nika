package cors

import (
	"fmt"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/sajadweb/nika"
)

// Config holds the CORS module configuration, fully decoupled from the underlying package.
type Config struct {
	AllowOrigins     []string
	AllowAllOrigins  bool
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	AllowOriginFunc  func(origin string) bool
	MaxAge           time.Duration
}

// Cors is the main module structure that holds the final Gin handler.
type Cors struct {
	handler gin.HandlerFunc
}

// Setup instantiates the CORS module, registers the middleware.
func Setup(app *nika.App, cfg Config) (*Cors, error) {
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	app.Use(c.Middleware())
	return c, nil
}

// New handles the instantiation and validation of the configuration parameters.
func New(cfg Config) (*Cors, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	corsConfig := buildConfig(cfg)
	handler := cors.New(corsConfig)

	return &Cors{
		handler: handler,
	}, nil
}

// Middleware returns the prepared Gin handler function.
func (c *Cors) Middleware() gin.HandlerFunc {
	return c.handler
}

// validateConfig checks for logical configuration conflicts before building the middleware.
func validateConfig(cfg Config) error {
	// According to browser security specs and gin-contrib/cors documentation:
	// AllowAllOrigins and AllowCredentials cannot be true at the same time.
	if cfg.AllowAllOrigins && cfg.AllowCredentials {
		return fmt.Errorf("cors: allowCredentials and allowAllOrigins cannot be true at the same time")
	}
	return nil
}

// buildConfig maps our internal config to the gin-contrib/cors config and injects default values.
func buildConfig(cfg Config) cors.Config {
	c := cors.Config{
		AllowOrigins:     cfg.AllowOrigins,
		AllowMethods:     cfg.AllowMethods,
		AllowHeaders:     cfg.AllowHeaders,
		ExposeHeaders:    cfg.ExposeHeaders,
		AllowCredentials: cfg.AllowCredentials,
		AllowOriginFunc:  cfg.AllowOriginFunc,
		MaxAge:           cfg.MaxAge,
	}

	// Apply the flag to allow all origins
	if cfg.AllowAllOrigins {
		c.AllowAllOrigins = true
	}

	// Inject default values (similar to cors.DefaultConfig()) if left empty
	if len(c.AllowMethods) == 0 {
		c.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	}

	if len(c.AllowHeaders) == 0 {
		c.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type"}
	}

	if c.MaxAge <= 0 {
		// Default package max age (12 hours)
		c.MaxAge = 12 * time.Hour
	}

	return c
}
