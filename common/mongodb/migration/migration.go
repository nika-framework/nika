// Package migration provides a versioned migration runner for MongoDB.
// Migrations are Go functions that receive a *mongo.Database and can create
// indexes, transform documents, backfill fields, etc. Applied versions are
// tracked in a dedicated collection so re-running is idempotent.
package migration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nika-framework/nika/common/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// UpFn runs the forward migration.
type UpFn func(ctx context.Context, db *mongo.Database) error

// DownFn runs the rollback.
type DownFn func(ctx context.Context, db *mongo.Database) error

// Migration is a single forward+rollback unit.
type Migration struct {
	Version int64
	Name    string
	Up      UpFn
	Down    DownFn

	// Checksum optionally fingerprints the migration body. When both it and the
	// recorded value are set and they differ, the runner refuses to continue: the
	// database no longer matches the code that supposedly produced it, and every
	// later migration is written against the wrong assumption. Mongo migrations
	// are Go functions with no source text to hash, so this is opt-in.
	Checksum string
}

// Applied is one entry from the tracking collection.
type Applied struct {
	Version   int64     `bson:"version"`
	Name      string    `bson:"name"`
	AppliedAt time.Time `bson:"applied_at"`
	Checksum  string    `bson:"checksum,omitempty"`

	// Completed distinguishes a claim from a finished migration. A document
	// inserted but not yet completed means some process is running (or died
	// running) that version.
	Completed bool `bson:"completed"`
}

// ErrChecksumMismatch reports that an applied migration's body changed since it
// ran.
var ErrChecksumMismatch = errors.New("migration: checksum mismatch")

var (
	registryMu sync.Mutex
	registry   []*Migration
)

// Register adds a migration to the process-wide registry.
func Register(m *Migration) {
	if m == nil {
		panic("migration: cannot register nil migration")
	}
	if m.Version <= 0 {
		panic(fmt.Sprintf("migration: version must be positive (got %d)", m.Version))
	}
	if m.Up == nil {
		panic(fmt.Sprintf("migration %d: Up is required", m.Version))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registry {
		if existing.Version == m.Version {
			panic(fmt.Sprintf("migration: duplicate version %d (%q vs %q)",
				m.Version, existing.Name, m.Name))
		}
	}
	registry = append(registry, m)
}

// Registered returns all registered migrations sorted by version.
func Registered() []*Migration {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]*Migration, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// Reset clears the registry (for tests).
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}

// Migrator applies MongoDB migrations against the default database.
type Migrator struct {
	db         *mongodb.MongoDB
	collection string
	migs       []*Migration

	ensureOnce sync.Once
	ensureErr  error
}

// validateCollection rejects names MongoDB reserves or cannot store. `$` is
// disallowed in collection names, a NUL byte truncates the name, and the
// `system.` prefix names internal collections.
func validateCollection(name string) error {
	switch {
	case name == "":
		return errors.New("collection name is empty")
	case strings.ContainsAny(name, "$\x00"):
		return errors.New("collection name must not contain '$' or NUL")
	case strings.HasPrefix(name, "system."):
		return errors.New("collection name must not use the reserved 'system.' prefix")
	default:
		return nil
	}
}

// New returns a Migrator that uses the process-wide registry.
func New(db *mongodb.MongoDB) *Migrator {
	return NewWith(db, Registered())
}

// NewWith returns a Migrator bound to an explicit list of migrations.
func NewWith(db *mongodb.MongoDB, migs []*Migration) *Migrator {
	sorted := make([]*Migration, len(migs))
	copy(sorted, migs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	return &Migrator{
		db:         db,
		collection: "_migrations",
		migs:       sorted,
	}
}

// WithCollection overrides the tracking collection name.
// It panics on a name MongoDB cannot use.
func (m *Migrator) WithCollection(name string) *Migrator {
	if name != "" {
		if err := validateCollection(name); err != nil {
			panic(fmt.Sprintf("nika/migration: invalid tracking collection %q: %v", name, err))
		}
		m.collection = name
		m.ensureOnce = sync.Once{}
	}
	return m
}

// Ensure creates the unique index on the tracking collection's version field.
// That index is what makes concurrent migration runs safe: see UpN.
func (m *Migrator) Ensure(ctx context.Context) error {
	// Ensure is reached from Applied, which every other method calls; creating
	// the index once per Migrator keeps a round trip off each of them.
	m.ensureOnce.Do(func() {
		coll := m.db.DefaultDatabase().Collection(m.collection)
		_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "version", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("version_unique"),
		})
		if err != nil {
			m.ensureErr = fmt.Errorf("migration ensure index: %w", err)
		}
	})
	return m.ensureErr
}

// Applied lists all applied migrations, ordered by version ASC.
func (m *Migrator) Applied(ctx context.Context) ([]Applied, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	coll := m.db.DefaultDatabase().Collection(m.collection)
	cur, err := coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "version", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("migration find: %w", err)
	}
	defer cur.Close(ctx)

	var out []Applied
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("migration decode: %w", err)
	}
	return out, nil
}

// Pending returns migrations not yet applied.
func (m *Migrator) Pending(ctx context.Context) ([]*Migration, error) {
	applied, err := m.Applied(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(applied))
	for _, a := range applied {
		seen[a.Version] = struct{}{}
	}
	var pending []*Migration
	for _, mg := range m.migs {
		if _, ok := seen[mg.Version]; !ok {
			pending = append(pending, mg)
		}
	}
	return pending, nil
}

// Up applies every pending migration in order.
func (m *Migrator) Up(ctx context.Context) ([]int64, error) {
	return m.UpN(ctx, 0)
}

// UpN applies up to n pending migrations (n <= 0 means all).
func (m *Migrator) UpN(ctx context.Context, n int) ([]int64, error) {
	if err := m.Verify(ctx); err != nil {
		return nil, err
	}

	pending, err := m.Pending(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 && n < len(pending) {
		pending = pending[:n]
	}
	applied := make([]int64, 0, len(pending))
	db := m.db.DefaultDatabase()
	coll := db.Collection(m.collection)

	for _, mig := range pending {
		// Claim the version before running it. MongoDB has no advisory lock, so
		// the unique index on version is the mutual exclusion: two pods rolling
		// out together both compute the same pending set, and the loser's insert
		// fails with a duplicate key instead of applying the migration a second
		// time. Recording *after* the body, as this used to, gave both a clear run
		// — and a migration that creates an index or backfills documents is not
		// idempotent.
		claim := Applied{
			Version:   mig.Version,
			Name:      mig.Name,
			AppliedAt: time.Now().UTC(),
			Checksum:  mig.Checksum,
			Completed: false,
		}
		if _, err := coll.InsertOne(ctx, claim); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return applied, fmt.Errorf(
					"migration %d %q is already claimed by another process (or a previous run left it incomplete)",
					mig.Version, mig.Name)
			}
			return applied, fmt.Errorf("claim %d %q: %w", mig.Version, mig.Name, err)
		}

		if err := mig.Up(ctx, db); err != nil {
			// Release the claim so the migration can be retried. Mongo has no
			// transaction spanning the DDL-like operations a migration performs,
			// so partial effects may remain — the failure is reported either way.
			if _, delErr := coll.DeleteOne(ctx, bson.M{"version": mig.Version}); delErr != nil {
				return applied, fmt.Errorf(
					"apply migration %d %q: %w (and releasing the claim failed: %v)",
					mig.Version, mig.Name, err, delErr)
			}
			return applied, fmt.Errorf("apply migration %d %q: %w", mig.Version, mig.Name, err)
		}

		if _, err := coll.UpdateOne(ctx,
			bson.M{"version": mig.Version},
			bson.M{"$set": bson.M{"completed": true, "applied_at": time.Now().UTC()}},
		); err != nil {
			return applied, fmt.Errorf("record %d %q: %w", mig.Version, mig.Name, err)
		}
		applied = append(applied, mig.Version)
	}
	return applied, nil
}

// Verify reports whether every already-applied migration still matches the
// checksum recorded when it ran. Versions with no checksum on either side are
// skipped.
func (m *Migrator) Verify(ctx context.Context) error {
	applied, err := m.Applied(ctx)
	if err != nil {
		return err
	}
	recorded := make(map[int64]Applied, len(applied))
	for _, a := range applied {
		recorded[a.Version] = a
	}

	for _, mg := range m.migs {
		a, ok := recorded[mg.Version]
		if !ok || mg.Checksum == "" || a.Checksum == "" {
			continue
		}
		if a.Checksum != mg.Checksum {
			return fmt.Errorf("%w: migration %d %q was applied as %s but is now %s",
				ErrChecksumMismatch, mg.Version, mg.Name, a.Checksum, mg.Checksum)
		}
	}
	return nil
}

// Down rolls back the most recent applied migration.
func (m *Migrator) Down(ctx context.Context) (int64, error) {
	versions, err := m.DownN(ctx, 1)
	if err != nil || len(versions) == 0 {
		return 0, err
	}
	return versions[0], nil
}

// DownN rolls back the last n applied migrations, newest first.
func (m *Migrator) DownN(ctx context.Context, n int) ([]int64, error) {
	if n <= 0 {
		return nil, nil
	}
	applied, err := m.Applied(ctx)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[int64]*Migration, len(m.migs))
	for _, mg := range m.migs {
		byVersion[mg.Version] = mg
	}
	db := m.db.DefaultDatabase()
	coll := db.Collection(m.collection)

	rolled := make([]int64, 0, n)
	for i := len(applied) - 1; i >= 0 && len(rolled) < n; i-- {
		mig, ok := byVersion[applied[i].Version]
		if !ok {
			return rolled, fmt.Errorf("cannot rollback %d %q: migration not registered",
				applied[i].Version, applied[i].Name)
		}
		if mig.Down == nil {
			return rolled, fmt.Errorf("cannot rollback %d %q: no Down function",
				mig.Version, mig.Name)
		}
		if err := mig.Down(ctx, db); err != nil {
			return rolled, fmt.Errorf("rollback %d %q: %w", mig.Version, mig.Name, err)
		}
		if _, err := coll.DeleteOne(ctx, bson.M{"version": mig.Version}); err != nil {
			return rolled, fmt.Errorf("delete tracking %d: %w", mig.Version, err)
		}
		rolled = append(rolled, mig.Version)
	}
	return rolled, nil
}

// Status returns a human-readable report of migration state.
func (m *Migrator) Status(ctx context.Context) (string, error) {
	applied, err := m.Applied(ctx)
	if err != nil {
		return "", err
	}
	appliedSet := make(map[int64]Applied, len(applied))
	for _, a := range applied {
		appliedSet[a.Version] = a
	}
	var sb strings.Builder
	sb.WriteString("VERSION           NAME                          STATUS       APPLIED AT\n")
	for _, mg := range m.migs {
		if a, ok := appliedSet[mg.Version]; ok {
			fmt.Fprintf(&sb, "%-17d %-29s applied      %s\n",
				mg.Version, truncate(mg.Name, 29), a.AppliedAt.UTC().Format(time.RFC3339))
		} else {
			fmt.Fprintf(&sb, "%-17d %-29s pending      -\n",
				mg.Version, truncate(mg.Name, 29))
		}
	}
	return sb.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
