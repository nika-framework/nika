package nika

import (
	"os"
	"time"
)

// Default limits applied when the corresponding Config field is left zero.
const (
	DefaultMaxBodyBytes      int64         = 10 << 20 // 10 MiB
	DefaultReadHeaderTimeout time.Duration = 10 * time.Second
	DefaultIdleTimeout       time.Duration = 60 * time.Second
	DefaultShutdownTimeout   time.Duration = 15 * time.Second
	DefaultMaxHeaderBytes    int           = 1 << 20 // 1 MiB
)

// Config tunes the application server. Every field is optional: the zero value
// produces a hardened, production-ready server.
//
// The negative field names (Disable*) are deliberate — they keep the zero value
// safe, so a caller that writes nika.NewApp() gets the protective defaults
// rather than an unprotected server.
type Config struct {
	// Mode is the Gin mode: "release", "debug" or "test". Defaults to
	// "release" unless the GIN_MODE environment variable is set.
	Mode string

	// DisableRecovery removes the panic-recovery middleware. Leaving recovery
	// enabled means a panic in one handler returns 500 for that request instead
	// of taking the whole process down.
	DisableRecovery bool

	// DisableBodyLimit removes the request body size cap. Without a cap a
	// single client can stream an unbounded body and exhaust memory.
	DisableBodyLimit bool

	// MaxBodyBytes caps the request body size. Defaults to DefaultMaxBodyBytes.
	MaxBodyBytes int64

	// RequestLogger enables the built-in structured access log. Off by default
	// to keep the hot path allocation-free; enable it or install your own.
	RequestLogger bool

	// RequestID makes the app accept or generate an X-Request-ID per request and
	// expose it through RequestIDFrom.
	RequestID bool

	// DisableJSONFallbacks restores Gin's plain-text bodies for an unmatched
	// route (404) and an unmatched method (405). Leave it off so those answers
	// use the same JSON envelope as every other error and clients need to parse
	// only one shape. Set it when serving HTML or static files, where a plain
	// 404 page is what you want.
	DisableJSONFallbacks bool

	// SecurityHeaders adds a conservative set of response hardening headers
	// (nosniff, frame deny, referrer policy, and HSTS when served over TLS).
	SecurityHeaders bool

	// TrustedProxies lists the CIDRs or IPs of reverse proxies permitted to set
	// X-Forwarded-For / X-Real-IP. It is empty by default, which means
	// ClientIP() returns the real socket peer and cannot be spoofed. Set it to
	// your load balancer's range when running behind one — never to
	// "0.0.0.0/0".
	TrustedProxies []string

	// TrustedPlatform short-circuits client IP resolution to a platform header
	// such as gin.PlatformCloudflare or gin.PlatformGoogleAppEngine.
	TrustedPlatform string

	// ReadHeaderTimeout bounds how long the server waits for request headers.
	// It is the defence against Slowloris. Defaults to DefaultReadHeaderTimeout.
	ReadHeaderTimeout time.Duration

	// ReadTimeout bounds the time to read the entire request, body included.
	// Left at zero (no limit) by default: a whole-request deadline also applies
	// to hijacked connections, which would break WebSockets and long uploads.
	// Set it when the service only serves ordinary bounded requests.
	ReadTimeout time.Duration

	// WriteTimeout bounds the time to write the response. Left at zero by
	// default for the same reason as ReadTimeout — it would cut off SSE streams
	// and large file downloads. MaxBodyBytes and ReadHeaderTimeout carry the
	// baseline protection instead.
	WriteTimeout time.Duration

	// IdleTimeout bounds how long a keep-alive connection may stay idle.
	IdleTimeout time.Duration

	// MaxHeaderBytes caps the request header size.
	MaxHeaderBytes int

	// ShutdownTimeout bounds graceful shutdown before connections are forced
	// closed. Defaults to DefaultShutdownTimeout.
	ShutdownTimeout time.Duration

	// DisableGracefulShutdown makes Listen return as soon as the server stops
	// instead of intercepting SIGINT/SIGTERM and draining in-flight requests.
	DisableGracefulShutdown bool
}

// withDefaults returns a copy of cfg with every unset field filled in.
func (c Config) withDefaults() Config {
	if c.Mode == "" {
		if mode := os.Getenv("GIN_MODE"); mode != "" {
			c.Mode = mode
		} else {
			c.Mode = "release"
		}
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	return c
}
