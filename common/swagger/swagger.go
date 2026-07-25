// Package swagger mounts the generated OpenAPI UI.
//
// Publishing Swagger to the internet hands an attacker a complete, machine-
// readable map of the API: every route, every parameter, every field name and
// type, and often the admin-only endpoints that were never meant to be
// discoverable. Treat the docs endpoint as an internal tool — leave it off in
// production, or put it behind Guards.
package swagger

import (
	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

type Config struct {
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

	if !enabled(cfg) {
		return false
	}

	path := cfg.Path
	if path == "" {
		path = "/swagger/*any"
	}

	handler := ginSwagger.WrapHandler(
		swaggerFiles.Handler,
		ginSwagger.PersistAuthorization(cfg.PersistAuthorization),
	)

	handlers := make([]gin.HandlerFunc, 0, len(cfg.Guards)+1)
	handlers = append(handlers, cfg.Guards...)
	handlers = append(handlers, handler)

	app.GET(path, handlers...)
	return true
}

// enabled resolves the tri-state Enabled flag against the current gin mode.
func enabled(cfg *Config) bool {
	if cfg.Enabled != nil {
		return *cfg.Enabled
	}
	return gin.Mode() != gin.ReleaseMode
}
