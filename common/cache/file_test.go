package cache

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestFileProvider(t *testing.T) (*FileProvider, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	p, err := NewFileProvider(dir)
	if err != nil {
		t.Fatalf("NewFileProvider error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, dir
}

func TestFileProviderConformance(t *testing.T) {
	providerConformance(t, func(t *testing.T) Provider {
		p, _ := newTestFileProvider(t)
		return p
	})
}

// TestFileProviderPathTraversalKeysStayInsideDirectory is the regression test for
// the critical bug: FileProvider used to join the caller's raw key onto the cache
// directory, so a key containing ".." escaped it and turned any cache write into
// an arbitrary file write.
func TestFileProviderPathTraversalKeysStayInsideDirectory(t *testing.T) {
	ctx := context.Background()

	keys := []struct {
		name string
		key  string
	}{
		{"parent traversal", "../escaped"},
		{"deep traversal", "../../etc/cron.d/payload"},
		{"very deep traversal", strings.Repeat("../", 12) + "app/config"},
		{"absolute path", "/etc/passwd"},
		{"absolute path with traversal", "/../../etc/shadow"},
		{"dot dot only", ".."},
		{"nested subdirectory", "sessions/user/42"},
		{"windows separator", `..\..\windows\system32\drivers\etc\hosts`},
		{"trailing separator", "sessions/"},
		{"embedded newline", "key\nwith\nnewlines"},
		{"embedded null-ish", "key\x00suffix"},
		{"unicode", "کلید/../../خارج"},
		{"overlong", strings.Repeat("a", 5000)},
		{"dot", "."},
		{"tilde", "~/.ssh/authorized_keys"},
	}

	for _, tc := range keys {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cacheDir := filepath.Join(root, "cache")
			// Sibling directories a traversal would plausibly land in. Their
			// contents must be untouched afterwards.
			decoy := filepath.Join(root, "etc")
			if err := os.MkdirAll(decoy, 0o700); err != nil {
				t.Fatalf("MkdirAll error = %v", err)
			}
			decoyFile := filepath.Join(decoy, "passwd")
			if err := os.WriteFile(decoyFile, []byte("original"), 0o600); err != nil {
				t.Fatalf("WriteFile error = %v", err)
			}

			p, err := NewFileProvider(cacheDir)
			if err != nil {
				t.Fatalf("NewFileProvider error = %v", err)
			}
			defer p.Close()

			// The path must resolve inside the cache directory before anything is
			// written.
			got := p.keyPath(tc.key)
			rel, relErr := filepath.Rel(cacheDir, got)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("keyPath(%q) = %q, which is outside %q", tc.key, got, cacheDir)
			}
			if filepath.Dir(got) != cacheDir {
				t.Fatalf("keyPath(%q) = %q, want a direct child of %q", tc.key, got, cacheDir)
			}

			if err := p.Set(ctx, tc.key, "attacker-controlled", time.Minute); err != nil {
				t.Fatalf("Set(%q) error = %v, want nil", tc.key, err)
			}
			if _, err := p.SetNX(ctx, tc.key+"-nx", "attacker-controlled", time.Minute); err != nil {
				t.Fatalf("SetNX error = %v, want nil", err)
			}
			if _, err := p.Increment(ctx, tc.key+"-inc", 1); err != nil {
				t.Fatalf("Increment error = %v, want nil", err)
			}

			// Nothing may have been created anywhere under root except inside the
			// cache directory.
			walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				if path == decoyFile {
					return nil
				}
				if filepath.Dir(path) != cacheDir {
					t.Errorf("file created outside the cache directory: %q", path)
				}
				return nil
			})
			if walkErr != nil {
				t.Fatalf("WalkDir error = %v", walkErr)
			}

			// And the decoy must still hold its original bytes.
			data, err := os.ReadFile(decoyFile)
			if err != nil {
				t.Fatalf("ReadFile error = %v", err)
			}
			if string(data) != "original" {
				t.Fatalf("decoy file was overwritten: %q", data)
			}

			// The value must still be readable under its original key: hashing must
			// not break legitimate keys that merely look like paths.
			value, err := p.Get(ctx, tc.key)
			if err != nil {
				t.Fatalf("Get(%q) error = %v, want nil", tc.key, err)
			}
			if value != "attacker-controlled" {
				t.Fatalf("Get(%q) = %q, want the stored value", tc.key, value)
			}
		})
	}
}

func TestFileProviderKeyPathIsAHexHash(t *testing.T) {
	p, dir := newTestFileProvider(t)

	cases := []string{"a", "sessions/user/42", strings.Repeat("x", 4096), "../../etc"}
	seen := make(map[string]string, len(cases))

	for _, key := range cases {
		base := filepath.Base(p.keyPath(key))
		if len(base) != 64+len(fileEntrySuffix) {
			t.Fatalf("keyPath(%q) basename = %q, want 64 hex chars plus %q", key, base, fileEntrySuffix)
		}
		name := strings.TrimSuffix(base, fileEntrySuffix)
		for _, r := range name {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("keyPath(%q) basename %q contains a non-hex character %q", key, base, r)
			}
		}
		if other, dup := seen[base]; dup {
			t.Fatalf("keys %q and %q hashed to the same file %q", key, other, base)
		}
		seen[base] = key
	}

	// Same key, same path: the mapping has to be deterministic or nothing is
	// readable back.
	if p.keyPath("a") != p.keyPath("a") {
		t.Fatal("keyPath is not deterministic")
	}
	if filepath.Dir(p.keyPath("a")) != dir {
		t.Fatal("keyPath escaped the provider directory")
	}
}

func TestNewFileProviderCreatesADirectoryThatIsNotWorldReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	p, err := NewFileProvider(dir)
	if err != nil {
		t.Fatalf("NewFileProvider error = %v, want nil", err)
	}
	defer p.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700: a cache holding session data must not be group or world readable", perm)
	}
}

func TestNewFileProviderPropagatesErrors(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if _, err := NewFileProvider(""); err == nil {
			t.Fatal("NewFileProvider(\"\") error = nil, want an error")
		}
	})

	t.Run("path blocked by a regular file", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}

		// MkdirAll cannot create a directory under a regular file. The old
		// constructor discarded this error and handed back a provider whose every
		// Set failed later, at request time.
		if _, err := NewFileProvider(filepath.Join(blocker, "cache")); err == nil {
			t.Fatal("NewFileProvider error = nil, want an error")
		}
	})
}

func TestFileProviderStoresTheKeyInsideTheEntry(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestFileProvider(t)

	mustSet(t, p, "session:42", "value", time.Minute)

	raw, err := os.ReadFile(p.keyPath("session:42"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	var item FileItem
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if item.Key != "session:42" {
		t.Fatalf("stored key = %q, want %q", item.Key, "session:42")
	}

	// If the stored key does not match the requested one, the entry must not be
	// served. This is the guard against a hash-prefix collision.
	item.Key = "someone-elses-key"
	tampered, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	if err := os.WriteFile(p.keyPath("session:42"), tampered, 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	if _, err := p.Get(ctx, "session:42"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on a key mismatch = %v, want ErrNotFound", err)
	}
	if _, err := p.TTL(ctx, "session:42"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TTL on a key mismatch = %v, want ErrNotFound", err)
	}
}

func TestFileProviderSetLeavesNoTemporaryFiles(t *testing.T) {
	p, dir := newTestFileProvider(t)

	for i := 0; i < 5; i++ {
		mustSet(t, p, "k", "v", time.Minute)
	}
	if _, err := p.Increment(context.Background(), "counter", 3); err != nil {
		t.Fatalf("Increment error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != fileEntrySuffix {
			t.Fatalf("leftover scratch file %q: the atomic write must rename or remove its temp file", entry.Name())
		}
	}
	// Two keys, one file each: repeated Sets must replace, not accumulate.
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
}

func TestFileProviderGetRemovesAnExpiredEntryItReads(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestFileProvider(t)

	mustSet(t, p, "k", "v", 30*time.Millisecond)
	path := p.keyPath("k")
	time.Sleep(120 * time.Millisecond)

	if _, err := p.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat after expired read = %v, want the file to be gone", err)
	}
}

func TestFileProviderReclaimsACorruptEntry(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestFileProvider(t)

	path := p.keyPath("broken")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	// A truncated entry — what the old non-atomic Set left behind on a crash —
	// must not permanently shadow the key.
	ok, err := p.SetNX(ctx, "broken", "recovered", time.Minute)
	if err != nil {
		t.Fatalf("SetNX error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("SetNX = false, want true: an undecodable entry can never be served and must be reclaimable")
	}
	if got, _ := p.Get(ctx, "broken"); got != "recovered" {
		t.Fatalf("Get = %q, want \"recovered\"", got)
	}
}

func TestFileProviderJanitorRemovesExpiredEntries(t *testing.T) {
	p, dir := newTestFileProvider(t)

	if !p.StartJanitor(10 * time.Millisecond) {
		t.Fatal("StartJanitor = false, want true")
	}
	if p.StartJanitor(10 * time.Millisecond) {
		t.Fatal("second StartJanitor = true, want false: it must not start a second goroutine")
	}

	for _, key := range []string{"a", "b", "c"} {
		mustSet(t, p, key, "v", 30*time.Millisecond)
	}
	mustSet(t, p, "kept", "v", NoExpiry)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countEntries(t, dir) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := countEntries(t, dir); got != 1 {
		t.Fatalf("entry count = %d, want 1: the janitor should have removed the three expired entries", got)
	}
	if _, err := p.Get(context.Background(), "kept"); err != nil {
		t.Fatalf("Get(kept) error = %v, want nil", err)
	}
}

func TestFileProviderStartJanitorRejectsNonPositiveInterval(t *testing.T) {
	p, _ := newTestFileProvider(t)
	for _, interval := range []time.Duration{0, -time.Minute} {
		if p.StartJanitor(interval) {
			t.Fatalf("StartJanitor(%v) = true, want false", interval)
		}
	}
}

func TestFileProviderDeleteExpiredIgnoresForeignFiles(t *testing.T) {
	p, dir := newTestFileProvider(t)

	mustSet(t, p, "dead", "v", time.Nanosecond)
	mustSet(t, p, "alive", "v", time.Minute)

	foreign := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(foreign, []byte("not ours"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	removed, err := p.DeleteExpired()
	if err != nil {
		t.Fatalf("DeleteExpired error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteExpired = %d, want 1", removed)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("the janitor deleted a file it does not own: %v", err)
	}
}

func TestFileProviderPingRejectsANonDirectory(t *testing.T) {
	ctx := context.Background()
	p, dir := newTestFileProvider(t)

	if err := p.Ping(ctx); err != nil {
		t.Fatalf("Ping error = %v, want nil", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll error = %v", err)
	}
	if err := p.Ping(ctx); err == nil {
		t.Fatal("Ping error = nil after the directory vanished, want an error")
	}

	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if err := p.Ping(ctx); err == nil {
		t.Fatal("Ping error = nil when the path is a regular file, want an error")
	}
}

func TestFileProviderCloseStopsTheJanitor(t *testing.T) {
	p, _ := newTestFileProvider(t)
	p.StartJanitor(time.Millisecond)

	// Close waits for the goroutine, so `go test -race` reporting no leak here is
	// the assertion.
	if err := p.Close(); err != nil {
		t.Fatalf("Close error = %v, want nil", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close error = %v, want nil", err)
	}
}

func countEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == fileEntrySuffix {
			count++
		}
	}
	return count
}
