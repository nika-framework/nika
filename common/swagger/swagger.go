// Package swagger mounts the generated OpenAPI UI.
//
// Publishing Swagger to the internet hands an attacker a complete, machine-
// readable map of the API: every route, every parameter, every field name and
// type, and often the admin-only endpoints that were never meant to be
// discoverable. Treat the docs endpoint as an internal tool — leave it off in
// production, or put it behind Guards.
package swagger

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

type Config struct {
	// Path is where the docs are served. Both the plain form ("/docs") and gin's
	// wildcard form ("/docs/*any") are accepted and mean the same thing; the base
	// path itself redirects to the UI, so "/docs" works and nobody has to
	// remember "/docs/index.html". Defaults to "/swagger".
	Path                 string
	PersistAuthorization bool

	// Enabled controls whether the docs route is mounted at all. When nil the
	// docs are mounted only outside gin.ReleaseMode, so a production build does
	// not expose them by accident. Set it explicitly to override — including to
	// true, if the docs are deliberately public.
	Enabled *bool

	// Guards run before the docs handler. This is where basic auth belongs
	// (gin.BasicAuth), or an IP allow-list, or a session check. Mounting with no
	// guard in an environment reachable from outside is a deliberate choice, not a
	// default.
	Guards []gin.HandlerFunc
}

// Enable returns a pointer suitable for Config.Enabled, for callers that want to
// force the docs on or off inline.
func Enable(enabled bool) *bool { return &enabled }

// Setup mounts the Swagger UI and reports whether it did.
//
// It previously mounted unconditionally and with no access control, so any app
// that imported this package published its entire API surface on /swagger in
// every environment.
func Setup(app *nika.App, cfg *Config) bool {
	if cfg == nil {
		cfg = &Config{}
	}

	base := basePath(cfg.Path)

	if !enabled(cfg) {
		// Skipping quietly turns into a 404 the caller has no way to explain —
		// and because nika defaults to release mode, that is what happens to
		// anyone who calls Setup without touching Enabled or GIN_MODE.
		fmt.Printf("⚠️  Swagger: docs not mounted at %s/ — gin is in %q mode. "+
			"Pass swagger.Config{Enabled: swagger.Enable(true)}, or run with GIN_MODE=debug / nika.Config{Mode: \"debug\"}.\n",
			base, gin.Mode())
		return false
	}

	docs := ginSwagger.WrapHandler(
		swaggerFiles.Handler,
		ginSwagger.PersistAuthorization(cfg.PersistAuthorization),
	)

	// gin-swagger only answers requests that name a UI asset, so "/docs" and
	// "/docs/" get its bare "404 page not found" instead of the UI. Send both to
	// index.html rather than making every user type the filename.
	index := base + "/index.html"
	handler := func(c *gin.Context) {
		if any := c.Param("any"); any == "" || any == "/" {
			c.Redirect(http.StatusFound, index)
			return
		}
		docs(c)
	}

	app.GET(base+"/*any", withGuards(cfg.Guards, handler)...)
	if base != "" {
		app.GET(base, withGuards(cfg.Guards, func(c *gin.Context) {
			c.Redirect(http.StatusFound, index)
		})...)
	}
	return true
}

// basePath normalises Config.Path into a prefix with no trailing slash and no
// wildcard segment, so that "/docs", "/docs/", "/docs/*any" and "/docs/*path"
// all describe the same mount point.
func basePath(path string) string {
	if path == "" {
		path = "/swagger"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if i := strings.Index(path, "/*"); i >= 0 {
		path = path[:i]
	}
	return strings.TrimSuffix(path, "/")
}

// withGuards puts the guards in front of handler, as one chain.
func withGuards(guards []gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	chain := make([]gin.HandlerFunc, 0, len(guards)+1)
	chain = append(chain, guards...)
	return append(chain, handler)
}

// enabled resolves the tri-state Enabled flag against the current gin mode.
func enabled(cfg *Config) bool {
	if cfg.Enabled != nil {
		return *cfg.Enabled
	}
	return gin.Mode() != gin.ReleaseMode
}
