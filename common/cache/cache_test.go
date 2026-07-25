package cache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
)

func newTestApp() *nika.App {
	return nika.NewApp(nika.Config{Mode: gin.TestMode})
}

func TestSetupDriverSelection(t *testing.T) {
	cases := []struct {
		name     string
		driver   string
		url      func(t *testing.T) string
		wantErr  bool
		wantType any
	}{
		{"memory", "memory", func(t *testing.T) string { return "" }, false, (*MemoryProvider)(nil)},
		{
			"file",
			"file",
			func(t *testing.T) string { return filepath.Join(t.TempDir(), "cache") },
			false,
			(*FileProvider)(nil),
		},
		{"memcached is not implemented", "memcached", func(t *testing.T) string { return "" }, true, nil},
		{"unknown driver", "mystery", func(t *testing.T) string { return "" }, true, nil},
		{"empty driver", "", func(t *testing.T) string { return "" }, true, nil},
		// A bad DSN must surface as an error rather than a half-built provider.
		{"redis with an unusable url", "redis", func(t *testing.T) string { return "not-a-redis-url" }, true, nil},
		{"file with an empty path", "file", func(t *testing.T) string { return "" }, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp()

			cache, err := Setup(app, Config{Driver: tc.driver, URL: tc.url(t)})
			if tc.wantErr {
				if err == nil {
					t.Fatal("Setup error = nil, want an error")
				}
				if cache != nil {
					t.Fatal("Setup returned a Cache alongside the error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Setup error = %v, want nil", err)
			}
			t.Cleanup(func() { _ = cache.Provider.Close() })

			switch tc.wantType.(type) {
			case *MemoryProvider:
				if _, ok := cache.Provider.(*MemoryProvider); !ok {
					t.Fatalf("provider = %T, want *MemoryProvider", cache.Provider)
				}
			case *FileProvider:
				if _, ok := cache.Provider.(*FileProvider); !ok {
					t.Fatalf("provider = %T, want *FileProvider", cache.Provider)
				}
			}
		})
	}
}

// TestSetupRegistersTheProviderUnderTheInterface covers the wiring that lets a
// consumer depend on cache.Provider and be handed a MemoryProvider in tests
// without changing a line of its own code.
func TestSetupRegistersTheProviderUnderTheInterface(t *testing.T) {
	app := newTestApp()

	cache, err := Setup(app, Config{Driver: "memory"})
	if err != nil {
		t.Fatalf("Setup error = %v", err)
	}
	t.Cleanup(func() { _ = cache.Provider.Close() })

	resolvedCache, ok := nika.Resolve[*Cache](app)
	if !ok {
		t.Fatal("Resolve[*Cache] = not found, want the registered Cache")
	}
	if resolvedCache != cache {
		t.Fatal("Resolve[*Cache] returned a different instance")
	}

	resolvedProvider, ok := nika.Resolve[Provider](app)
	if !ok {
		t.Fatal("Resolve[Provider] = not found, want the provider registered under the interface")
	}
	if resolvedProvider != cache.Provider {
		t.Fatal("Resolve[Provider] returned a different instance")
	}

	// And it must actually work through the interface.
	if err := resolvedProvider.Set(context.Background(), "k", 1, time.Minute); err != nil {
		t.Fatalf("Set through the interface error = %v", err)
	}
}

func TestSetupStartsTheJanitorOnlyWhenConfigured(t *testing.T) {
	cases := []struct {
		name        string
		interval    time.Duration
		wantJanitor bool
	}{
		{"opted out", 0, false},
		{"opted in", 50 * time.Millisecond, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp()
			cache, err := Setup(app, Config{Driver: "memory", CleanupInterval: tc.interval})
			if err != nil {
				t.Fatalf("Setup error = %v", err)
			}
			provider := cache.Provider.(*MemoryProvider)
			t.Cleanup(func() { _ = provider.Close() })

			// StartJanitor returns false when one is already running, so a second
			// call reveals whether Setup started one.
			startedNow := provider.StartJanitor(time.Second)
			if tc.wantJanitor && startedNow {
				t.Fatal("Setup did not start a janitor despite CleanupInterval being set")
			}
			if !tc.wantJanitor && !startedNow {
				t.Fatal("Setup started a janitor even though CleanupInterval was zero")
			}
		})
	}
}
