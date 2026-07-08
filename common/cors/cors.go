package cors

import (
	"time"

	ginCors "github.com/gin-contrib/cors" // Aliased to avoid package name collision
	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// Config holds the CORS module configuration, fully decoupled from the underlying package.
// This now includes ALL attributes from github.com/gin-contrib/cors.
type Config struct {
	AllowAllOrigins        bool
	AllowOrigins           []string
	AllowMethods           []string
	AllowHeaders           []string
	CustomSchemas          []string
	ExposeHeaders          []string
	AllowOriginFunc        func(origin string) bool
	MaxAge                 time.Duration
	AllowCredentials       bool
	AllowPrivateNetwork    bool
	AllowWildcard          bool
	AllowBrowserExtensions bool
	AllowWebSockets        bool
	AllowFiles             bool
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

	corsConfig := buildConfig(cfg)
	handler := ginCors.New(corsConfig) // Using the alias

	return &Cors{
		handler: handler,
	}, nil
}

// Middleware returns the prepared Gin handler function.
func (c *Cors) Middleware() gin.HandlerFunc {
	return c.handler
}

// buildConfig maps our internal config to the gin-contrib/cors config and injects default values.
func buildConfig(cfg Config) ginCors.Config { // Using the alias
	c := ginCors.DefaultConfig()

	// Inject default values (similar to cors.DefaultConfig()) if left empty
	if len(cfg.AllowOrigins) > 0 {
		c.AllowOrigins = cfg.AllowOrigins
	}

	if len(cfg.AllowMethods) > 0 {
		c.AllowMethods = cfg.AllowMethods
	}

	if len(cfg.AllowHeaders) > 0 {
		c.AllowHeaders = cfg.AllowHeaders
	}

	if len(cfg.ExposeHeaders) > 0 {
		c.ExposeHeaders = cfg.ExposeHeaders
	}

	if len(cfg.CustomSchemas) > 0 {
		c.CustomSchemas = cfg.CustomSchemas
	}

	if cfg.AllowOriginFunc != nil {
		c.AllowOriginFunc = cfg.AllowOriginFunc
	}
	if cfg.MaxAge > 0 {
		c.MaxAge = cfg.MaxAge
	}

	if cfg.AllowCredentials {
		c.AllowCredentials = cfg.AllowCredentials
	}
	if cfg.AllowPrivateNetwork {
		c.AllowPrivateNetwork = cfg.AllowPrivateNetwork
	}
	if cfg.AllowWildcard {
		c.AllowWildcard = cfg.AllowWildcard
	}
	if cfg.AllowBrowserExtensions {
		c.AllowBrowserExtensions = cfg.AllowBrowserExtensions
	}
	if cfg.AllowWebSockets {
		c.AllowWebSockets = cfg.AllowWebSockets
	}
	if cfg.AllowFiles {
		c.AllowFiles = cfg.AllowFiles
	}
	if cfg.AllowAllOrigins {
		c.AllowAllOrigins = cfg.AllowAllOrigins
	}

	return c
}
