package microservice

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

// benchServer builds a server whose handler does the least possible work, so the
// numbers describe the framework's overhead rather than the handler's.
func benchServer(b *testing.B, patterns ...string) (*MemoryTransport, *Server) {
	b.Helper()

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := NewMemory()

	server, err := Setup(app, Config{Transport: transport})
	if err != nil {
		b.Fatalf("Setup returned %v", err)
	}

	router := server.Router(TransportMemory)
	for _, pattern := range patterns {
		route := &Route{Pattern: Pattern(pattern), Transport: TransportMemory,
			Controller: "Bench", Field: "Handler"}
		if err := router.Add(route); err != nil {
			b.Fatalf("Add(%q) returned %v", pattern, err)
		}
		server.dispatch.mount(route, []gin.HandlerFunc{
			func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) },
		})
	}

	if err := app.Start(context.Background()); err != nil {
		b.Fatalf("Start returned %v", err)
	}
	b.Cleanup(func() { _ = server.Stop(context.Background()) })

	return transport, server
}

// BenchmarkRouterResolveExact measures the fast path: a literal subject is a
// single map lookup.
func BenchmarkRouterResolveExact(b *testing.B) {
	router := NewRouter()
	for _, pattern := range []string{"user_created", "user_updated", "user_deleted", "users", "orders"} {
		if err := router.Add(route(pattern)); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, found := router.Resolve("user_created"); !found {
			b.Fatal("no match")
		}
	}
}

// BenchmarkRouterResolveWildcardCached measures a repeated wildcard subject,
// which is what real traffic looks like once the resolution cache is warm.
func BenchmarkRouterResolveWildcardCached(b *testing.B) {
	router := NewRouter()
	for _, pattern := range []string{"user_*", "order_*", "invoice_*", "*"} {
		if err := router.Add(route(pattern)); err != nil {
			b.Fatal(err)
		}
	}
	router.Resolve("user_23") // warm

	b.ReportAllocs()
	for b.Loop() {
		if _, found := router.Resolve("user_23"); !found {
			b.Fatal("no match")
		}
	}
}

// BenchmarkRouterResolveWildcardCold measures the uncached path — every subject
// distinct, which is also the hostile case the cache bound exists for.
func BenchmarkRouterResolveWildcardCold(b *testing.B) {
	router := NewRouter()
	for _, pattern := range []string{"user_*", "order_*", "invoice_*"} {
		if err := router.Add(route(pattern)); err != nil {
			b.Fatal(err)
		}
	}

	subjects := make([]string, 512)
	for i := range subjects {
		subjects[i] = "user_" + itoa(i)
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		router.Resolve(subjects[i%len(subjects)])
		i++
	}
}

func BenchmarkPatternMatch(b *testing.B) {
	cases := []struct {
		name    string
		pattern Pattern
		subject string
	}{
		{name: "literal", pattern: "user_created", subject: "user_created"},
		{name: "trailing_star", pattern: "user_*", subject: "user_1234567890"},
		{name: "middle_star", pattern: "user_*_v2", subject: "user_1234567890_v2"},
		{name: "no_match", pattern: "user_*_v2", subject: "user_1234567890_v3"},
	}

	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				test.pattern.Match(test.subject)
			}
		})
	}
}

func BenchmarkEnvelopeEncodeDecode(b *testing.B) {
	env, err := NewEnvelope("user_created", map[string]any{
		"name":  "Ada Lovelace",
		"email": "ada@example.com",
		"age":   36,
	})
	if err != nil {
		b.Fatal(err)
	}
	env.WithHeader("Authorization", "Bearer token")

	b.Run("encode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := env.Encode(); err != nil {
				b.Fatal(err)
			}
		}
	})

	encoded, err := env.Encode()
	if err != nil {
		b.Fatal(err)
	}

	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := DecodeEnvelope(encoded); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDispatch is the number that matters: the whole path from an envelope
// to a reply, including routing, the gin chain, binding-ready request
// construction and response capture.
func BenchmarkDispatch(b *testing.B) {
	transport, _ := benchServer(b, "user_created", "user_*", "users")

	env, err := NewEnvelope("user_created", map[string]string{"name": "Ada"})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		reply, err := transport.Dispatch(ctx, env)
		if err != nil {
			b.Fatal(err)
		}
		if reply.Status != http.StatusOK {
			b.Fatalf("status = %d", reply.Status)
		}
	}
}

// BenchmarkDispatchWildcard shows the cost difference between a literal and a
// wildcard subject once the resolution cache is warm — they should be close, and
// a regression here means the cache stopped working.
func BenchmarkDispatchWildcard(b *testing.B) {
	transport, _ := benchServer(b, "user_created", "user_*", "users")

	env, err := NewEnvelope("user_9876", nil)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := transport.Dispatch(ctx, env); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDispatchParallel(b *testing.B) {
	transport, _ := benchServer(b, "user_created")

	env, err := NewEnvelope("user_created", map[string]string{"name": "Ada"})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if _, err := transport.Dispatch(ctx, env); err != nil {
				b.Fatal(err)
			}
		}
	})
}
