package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// fileEntrySuffix marks the files this provider owns, so the janitor never
// mistakes an in-flight atomic-write temp file for an entry.
const fileEntrySuffix = ".json"

// FileProvider is a cache backed by one file per key.
//
// It is safe for concurrent use within a single process. It is NOT a correct
// distributed lock: SetNX must read an existing entry to decide whether it has
// expired, and that read-then-replace is not atomic across processes, so two
// processes can both believe they acquired the same expired key. Use
// RedisProvider when SetNX is being used as a lock.
type FileProvider struct {
	path string

	// nxMu serializes SetNX, whose expired-entry reclaim is a read followed by a
	// write and would otherwise let two goroutines both win the same key.
	nxMu sync.Mutex

	closeOnce sync.Once
	stopMu    sync.Mutex
	stop      context.CancelFunc
	janitorWG sync.WaitGroup
}

// FileItem is the on-disk representation of a cache entry.
type FileItem struct {
	// Key is the original, unhashed cache key. The filename is a hash, so this is
	// the only way Get can confirm the file it opened belongs to the key it was
	// asked for.
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
	NoExpiry  bool      `json:"no_expiry"`
}

func (i FileItem) expired() bool {
	return !i.NoExpiry && !time.Now().Before(i.ExpiresAt)
}

// NewFileProvider creates the cache directory and returns a provider rooted at
// it.
//
// The directory is 0700, not 0755: a cache routinely holds session tokens,
// password-reset codes and rendered private data, and on a shared host 0755 makes
// all of it world-readable. The MkdirAll error is now returned instead of
// discarded — swallowing it produced a provider whose every Set failed later, at
// request time, far from the cause.
func NewFileProvider(path string) (*FileProvider, error) {
	if path == "" {
		return nil, errors.New("cache: file provider needs a directory path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return &FileProvider{path: path}, nil
}

// keyPath maps a cache key to its on-disk path.
//
// The filename is the hex SHA-256 of the key. The previous implementation used
// filepath.Join(f.path, key+".json") on the caller's raw key, so a key such as
// "../../etc/cron.d/payload" or "../../../app/config" escaped the cache directory
// and turned any cache write into an arbitrary file write with the server's
// privileges. Hashing eliminates the class of bug instead of blacklisting
// sequences: the output is always 64 hex characters, so no input — traversal,
// absolute path, NUL byte, separator, or overlong name — can name a path outside
// f.path.
func (f *FileProvider) keyPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(f.path, hex.EncodeToString(sum[:])+fileEntrySuffix)
}

func (f *FileProvider) encode(key string, value any, exp time.Duration) ([]byte, error) {
	str, err := marshalValue(value)
	if err != nil {
		return nil, err
	}

	item := FileItem{Key: key, Value: str}
	if exp <= 0 {
		item.NoExpiry = true
	} else {
		item.ExpiresAt = time.Now().Add(exp)
	}
	return json.Marshal(item)
}

// readItem loads an entry without applying expiry, reporting ErrNotFound when the
// file is absent.
func (f *FileProvider) readItem(path string) (FileItem, error) {
	var item FileItem
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return item, ErrNotFound
		}
		return item, err
	}
	if err := json.Unmarshal(data, &item); err != nil {
		return item, err
	}
	return item, nil
}

// writeTemp writes data to a fresh scratch file in the cache directory and
// returns its name. The caller must rename, link, or remove it.
func (f *FileProvider) writeTemp(data []byte) (string, error) {
	tmp, err := os.CreateTemp(f.path, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()

	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}

	// CreateTemp already uses 0600; being explicit keeps the entry under the same
	// "not world-readable" rule as the directory even if that changes.
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	// Deliberately no fsync. Rename is atomic with respect to readers without it,
	// which is the property that matters here: a reader never sees a fragment.
	// Durability across a power cut is not worth an fsync — tens of milliseconds
	// on some filesystems — on every cache write, because a lost cache entry is
	// just a miss.
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

// writeAtomic replaces path in a single step.
//
// Set previously called os.WriteFile, which truncates first: a crash or a full
// disk mid-write left a half-written file that every later Get failed to
// unmarshal, and the key stayed poisoned until something overwrote it. Writing a
// temp file in the same directory and renaming means a reader sees either the old
// entry or the new one, never a fragment.
func (f *FileProvider) writeAtomic(path string, data []byte) error {
	tmpName, err := f.writeTemp(data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (f *FileProvider) Set(ctx context.Context, key string, value any, exp time.Duration) error {
	if key == "" {
		return ErrKeyEmpty
	}
	data, err := f.encode(key, value, exp)
	if err != nil {
		return err
	}
	return f.writeAtomic(f.keyPath(key), data)
}

// SetNX stores value only when key is free, and reports whether it stored.
//
// An exclusive create alone is not enough: it fails with EEXIST for an entry that
// expired an hour ago just as readily as for a live one, so a caller using SetNX
// as a lock could never re-acquire a lock whose holder had died — a permanent
// deadlock. On a collision we therefore inspect the existing entry and reclaim it
// when it has expired, or when it is unreadable and so can never be served again.
//
// The claim is made by hard-linking a fully written temp file into place rather
// than by O_EXCL followed by a write. The latter publishes a zero-length file for
// the duration of the write, and a concurrent SetNX that read it in that window
// saw an undecodable entry, judged the key reclaimable, and became a second
// winner. Linking makes the entry appear complete or not at all.
//
// The reclaim path is still a read followed by a rename, so it is single-process
// safe only; see the FileProvider doc comment.
func (f *FileProvider) SetNX(ctx context.Context, key string, value any, exp time.Duration) (bool, error) {
	if key == "" {
		return false, ErrKeyEmpty
	}
	data, err := f.encode(key, value, exp)
	if err != nil {
		return false, err
	}

	path := f.keyPath(key)

	// Serialize in-process. The expiry check below is a read followed by a write,
	// so without this two goroutines could both decide the same expired key was
	// theirs.
	f.nxMu.Lock()
	defer f.nxMu.Unlock()

	tmpName, err := f.writeTemp(data)
	if err != nil {
		return false, err
	}
	// After a successful Link the temp file is a second name for the same inode
	// and must go; after a successful Rename it no longer exists and this is a
	// no-op.
	defer func() { _ = os.Remove(tmpName) }()

	if err := os.Link(tmpName, path); err == nil {
		return true, nil
	}

	// Link failed either because the key is taken (EEXIST) or because the
	// filesystem has no hard links at all (EPERM/ENOTSUP on some FUSE and network
	// mounts). Both cases fall through to the same question — is the existing
	// entry still live? — and the rename below covers the no-hard-links case,
	// whose lost cross-process atomicity this provider never promised anyway.
	item, readErr := f.readItem(path)
	switch {
	case readErr == nil && !item.expired():
		return false, nil
	case readErr != nil && !errors.Is(readErr, ErrNotFound) && !isCorrupt(readErr):
		return false, readErr
	}

	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

// isCorrupt reports whether err means "the file exists but cannot be decoded", in
// which case the entry is dead weight and may be reclaimed.
func isCorrupt(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func (f *FileProvider) Get(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", ErrKeyEmpty
	}

	path := f.keyPath(key)
	item, err := f.readItem(path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}

	if item.expired() {
		_ = os.Remove(path)
		return "", ErrNotFound
	}

	// The filename is only a hash of the key, so confirm the entry really is the
	// one asked for. Full SHA-256 makes a mismatch impossible today; the check
	// keeps the invariant honest if the directory is ever sharded by hash prefix,
	// where truncated names could collide.
	if item.Key != key {
		return "", ErrNotFound
	}

	return item.Value, nil
}

// Delete removes key. Removing an absent key is not an error, matching Redis DEL.
func (f *FileProvider) Delete(ctx context.Context, key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	if err := os.Remove(f.keyPath(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Increment adds delta to the counter at key, preserving its remaining TTL.
//
// Single-process only: read-modify-write on a file cannot be made atomic across
// processes without locking. Use RedisProvider for a shared counter.
func (f *FileProvider) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}

	path := f.keyPath(key)

	var current int64
	exp := NoExpiry

	item, err := f.readItem(path)
	switch {
	case err == nil && !item.expired() && item.Key == key:
		parsed, parseErr := strconv.ParseInt(item.Value, 10, 64)
		if parseErr != nil {
			return 0, errors.New("cache: value at key is not an integer")
		}
		current = parsed
		if !item.NoExpiry {
			// Keep the original deadline. A counter whose TTL restarted on every
			// increment would never expire under sustained traffic — precisely the
			// case rate limiting depends on.
			if remaining := time.Until(item.ExpiresAt); remaining > 0 {
				exp = remaining
			}
		}
	case err != nil && !errors.Is(err, ErrNotFound) && !isCorrupt(err):
		return 0, err
	}

	next := current + delta
	data, err := f.encode(key, strconv.FormatInt(next, 10), exp)
	if err != nil {
		return 0, err
	}
	if err := f.writeAtomic(path, data); err != nil {
		return 0, err
	}
	return next, nil
}

func (f *FileProvider) TTL(ctx context.Context, key string) (time.Duration, error) {
	if key == "" {
		return 0, ErrKeyEmpty
	}

	item, err := f.readItem(f.keyPath(key))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if item.expired() || item.Key != key {
		return 0, ErrNotFound
	}
	if item.NoExpiry {
		return NoExpiry, nil
	}
	return time.Until(item.ExpiresAt), nil
}

// StartJanitor begins periodic removal of expired entries and reports whether it
// started one.
//
// Without it the directory only shrinks when an expired key happens to be read,
// so a cache keyed by anything unbounded (session id, request path) grows until
// the filesystem runs out of inodes. It is opt-in because a short interval over a
// large directory is a real amount of I/O. Close stops it.
func (f *FileProvider) StartJanitor(interval time.Duration) bool {
	if interval <= 0 {
		return false
	}

	f.stopMu.Lock()
	defer f.stopMu.Unlock()
	if f.stop != nil {
		return false
	}

	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel

	f.janitorWG.Add(1)
	go func() {
		defer f.janitorWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = f.DeleteExpired()
			}
		}
	}()
	return true
}

// DeleteExpired removes every expired entry and returns how many it removed. It
// is exported so a caller can run cleanup on its own schedule rather than pay for
// a background goroutine.
func (f *FileProvider) DeleteExpired() (int, error) {
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != fileEntrySuffix {
			continue
		}
		path := filepath.Join(f.path, entry.Name())
		item, readErr := f.readItem(path)
		if readErr != nil && !isCorrupt(readErr) {
			continue
		}
		if readErr == nil && !item.expired() {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed, nil
}

// Ping verifies the cache directory is still a usable directory.
func (f *FileProvider) Ping(ctx context.Context) error {
	info, err := os.Stat(f.path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("cache: file provider path is not a directory")
	}
	return nil
}

// Close stops the janitor. It is safe to call repeatedly; sync.Once plus the
// waitgroup guarantee the goroutine is gone by the time Close returns, so neither
// a test nor a shutdown hook can leak it.
func (f *FileProvider) Close() error {
	f.closeOnce.Do(func() {
		f.stopMu.Lock()
		stop := f.stop
		f.stopMu.Unlock()
		if stop != nil {
			stop()
		}
		f.janitorWG.Wait()
	})
	return nil
}
