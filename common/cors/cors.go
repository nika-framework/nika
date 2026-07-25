// Package cors configures cross-origin resource sharing for a Nika app.
package cors

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
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

// ErrCredentialsWithAllOrigins reports the configuration the CORS specification
// forbids: credentialed requests combined with a wildcard origin.
var ErrCredentialsWithAllOrigins = errors.New(
	"cors: AllowCredentials cannot be combined with AllowAllOrigins (or a \"*\" entry in AllowOrigins): " +
		"the CORS specification forbids Access-Control-Allow-Origin: * on a credentialed response, and any " +
		"server that does send both lets every site on the internet read authenticated responses from this API. " +
		"List the exact origins that need cookies, or set AllowOriginFunc",
)

// originRegexpForm matches gin-contrib's /regexp/ origin syntax, which is not a
// URL and must not be parsed as one.
var originRegexpForm = regexp.MustCompile(`^/(.+)/[gimuy]?$`)

// Setup instantiates the CORS module, registers the middleware.
func Setup(app *nika.App, cfg Config) (*Cors, error) {
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	app.Use(c.Middleware())
	return c, nil
}

// New validates the configuration and builds the middleware.
//
// Validation happens before the underlying middleware is constructed for two
// reasons: gin-contrib/cors reacts to a bad configuration by panicking, and its
// own checks do not cover the wildcard-plus-credentials combination that actually
// breaks the security model.
func New(cfg Config) (*Cors, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}

	corsConfig := buildConfig(cfg)

	// Run the library's own validation explicitly. newCors() panics on failure,
	// and a panic during bootstrap is a worse diagnostic than a returned error —
	// notably for the "no origins configured at all" case, which is easy to reach
	// by leaving Config zero.
	if err := corsConfig.Validate(); err != nil {
		return nil, fmt.Errorf("cors: %w", err)
	}

	handler := ginCors.New(corsConfig) // Using the alias

	return &Cors{
		handler: handler,
	}, nil
}

// validate rejects configurations that are unsafe or silently ineffective.
func validate(cfg Config) error {
	// The dangerous combination. gin-contrib will happily emit
	// "Access-Control-Allow-Origin: *" together with
	// "Access-Control-Allow-Credentials: true"; modern browsers reject that pair,
	// but non-browser clients and older or patched stacks do not, and it is a
	// configuration nobody intends.
	if cfg.AllowCredentials {
		if cfg.AllowAllOrigins {
			return ErrCredentialsWithAllOrigins
		}
		for _, origin := range cfg.AllowOrigins {
			// A bare "*" in the list is the same thing by another route:
			// gin-contrib promotes it to AllowAllOrigins internally.
			if strings.TrimSpace(origin) == "*" {
				return ErrCredentialsWithAllOrigins
			}
		}
	}

	for _, origin := range cfg.AllowOrigins {
		if err := validateOrigin(origin, cfg); err != nil {
			return err
		}
	}

	return nil
}

// validateOrigin checks that an entry can actually match a browser's Origin
// header.
func validateOrigin(origin string, cfg Config) error {
	trimmed := strings.TrimSpace(origin)

	if trimmed == "" {
		return errors.New("cors: AllowOrigins contains an empty entry")
	}
	if trimmed == "*" {
		return nil
	}
	// Regexp-form entries are matched as patterns, not compared as URLs.
	if originRegexpForm.MatchString(trimmed) {
		return nil
	}

	if strings.Contains(trimmed, "*") {
		if !cfg.AllowWildcard {
			return fmt.Errorf(
				"cors: AllowOrigins entry %q contains '*' but AllowWildcard is false, so it can never match any request",
				trimmed,
			)
		}
		// parseWildcardRules panics on more than one '*'; catch it here instead.
		if strings.Count(trimmed, "*") > 1 {
			return fmt.Errorf("cors: AllowOrigins entry %q may contain at most one '*'", trimmed)
		}
		return nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("cors: AllowOrigins entry %q is not a valid origin: %w", trimmed, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf(
			"cors: AllowOrigins entry %q must be a full origin including the scheme, e.g. https://app.example.com",
			trimmed,
		)
	}
	// A browser sends only scheme://host[:port] in Origin, so anything past the
	// authority makes the entry dead configuration. Failing loudly beats an
	// operator debugging "CORS is blocking me" against a config that looks right.
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf(
			"cors: AllowOrigins entry %q must not include a path, query, fragment or trailing slash: "+
				"browsers send only scheme://host[:port] in the Origin header, so this entry can never match",
			trimmed,
		)
	}
	if parsed.User != nil {
		return fmt.Errorf("cors: AllowOrigins entry %q must not include credentials", trimmed)
	}

	return nil
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
