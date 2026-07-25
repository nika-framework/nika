// Package seed runs idempotent data-seed jobs against a MongoDB database.
//
// A seed is registered by name and, once applied, is recorded in a tracking
// collection so subsequent runs skip it. Seeds intended to re-run every
// time can set AlwaysRun.
package seed

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

// RunFn performs seeding work against the default database.
type RunFn func(ctx context.Context, db *mongo.Database) error

// Seed is one named data-seed operation.
type Seed struct {
	Name      string
	Order     int
	Run       RunFn
	AlwaysRun bool
}

var (
	registryMu sync.Mutex
	registry   []*Seed
)

// Register adds a seed to the process-wide registry.
func Register(s *Seed) {
	if s == nil {
		panic("seed: cannot register nil seed")
	}
	if s.Name == "" {
		panic("seed: Name is required")
	}
	if s.Run == nil {
		panic(fmt.Sprintf("seed %q: Run is required", s.Name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registry {
		if existing.Name == s.Name {
			panic(fmt.Sprintf("seed: duplicate name %q", s.Name))
		}
	}
	registry = append(registry, s)
}

// Registered returns registered seeds sorted by (Order, Name).
func Registered() []*Seed {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]*Seed, len(registry))
	copy(out, registry)
	sortSeeds(out)
	return out
}

// Reset clears the registry (for tests).
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = nil
}

func sortSeeds(s []*Seed) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Order != s[j].Order {
			return s[i].Order < s[j].Order
		}
		return s[i].Name < s[j].Name
	})
}

// Seeder applies seeds against a MongoDB database.
type Seeder struct {
	db         *mongodb.MongoDB
	collection string
	seeds      []*Seed

	ensureOnce sync.Once
	ensureErr  error
}

// New returns a Seeder using the current process-wide registry.
func New(db *mongodb.MongoDB) *Seeder {
	return NewWith(db, Registered())
}

// NewWith returns a Seeder bound to a specific list of seeds.
func NewWith(db *mongodb.MongoDB, seeds []*Seed) *Seeder {
	sorted := make([]*Seed, len(seeds))
	copy(sorted, seeds)
	sortSeeds(sorted)
	return &Seeder{
		db:         db,
		collection: "_seeds",
		seeds:      sorted,
	}
}

// WithCollection overrides the tracking collection.
// It panics on a name MongoDB cannot use.
func (s *Seeder) WithCollection(name string) *Seeder {
	if name != "" {
		if err := validateCollection(name); err != nil {
			panic(fmt.Sprintf("nika/seed: invalid tracking collection %q: %v", name, err))
		}
		s.collection = name
		s.ensureOnce = sync.Once{}
	}
	return s
}

// validateCollection rejects names MongoDB reserves or cannot store.
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

// Ensure creates the unique index on the tracking collection's name. That index
// is what makes a once-only seed exclusive across processes: see runOne.
func (s *Seeder) Ensure(ctx context.Context) error {
	s.ensureOnce.Do(func() {
		coll := s.db.DefaultDatabase().Collection(s.collection)
		_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("name_unique"),
		})
		if err != nil {
			s.ensureErr = fmt.Errorf("seed ensure: %w", err)
		}
	})
	return s.ensureErr
}

// AppliedNames returns the set of applied seed names.
func (s *Seeder) AppliedNames(ctx context.Context) (map[string]struct{}, error) {
	if err := s.Ensure(ctx); err != nil {
		return nil, err
	}
	coll := s.db.DefaultDatabase().Collection(s.collection)
	cur, err := coll.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("seed find: %w", err)
	}
	defer cur.Close(ctx)

	out := map[string]struct{}{}
	for cur.Next(ctx) {
		var row struct {
			Name string `bson:"name"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, fmt.Errorf("seed decode: %w", err)
		}
		out[row.Name] = struct{}{}
	}
	return out, cur.Err()
}

// Run executes every pending seed (plus AlwaysRun seeds).
func (s *Seeder) Run(ctx context.Context) ([]string, error) {
	applied, err := s.AppliedNames(ctx)
	if err != nil {
		return nil, err
	}
	ran := make([]string, 0, len(s.seeds))
	for _, seed := range s.seeds {
		if _, ok := applied[seed.Name]; ok && !seed.AlwaysRun {
			continue
		}
		// A once-only seed claims its name exclusively so a concurrent process
		// cannot insert the same reference data twice.
		if err := s.runOne(ctx, seed, !seed.AlwaysRun); err != nil {
			return ran, fmt.Errorf("seed %q: %w", seed.Name, err)
		}
		ran = append(ran, seed.Name)
	}
	return ran, nil
}

// RunOnly executes only the named seeds.
func (s *Seeder) RunOnly(ctx context.Context, names ...string) ([]string, error) {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	ran := make([]string, 0, len(names))
	for _, seed := range s.seeds {
		if _, ok := want[seed.Name]; !ok {
			continue
		}
		// RunOnly is documented to run regardless of prior state, so it upserts
		// its tracking row instead of claiming it.
		if err := s.runOne(ctx, seed, false); err != nil {
			return ran, fmt.Errorf("seed %q: %w", seed.Name, err)
		}
		ran = append(ran, seed.Name)
	}
	return ran, nil
}

func (s *Seeder) runOne(ctx context.Context, seed *Seed, exclusive bool) error {
	if err := s.Ensure(ctx); err != nil {
		return err
	}

	db := s.db.DefaultDatabase()
	coll := db.Collection(s.collection)

	// Claim the name *before* running the body. With the unique index in place,
	// a second process racing this one fails on the duplicate key rather than
	// inserting the same seed data again; recording afterwards let both run.
	if exclusive {
		if _, err := coll.InsertOne(ctx, bson.M{
			"name":       seed.Name,
			"applied_at": time.Now().UTC(),
		}); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return fmt.Errorf("already applied or claimed by another process")
			}
			return fmt.Errorf("record applied: %w", err)
		}
	} else if _, err := coll.UpdateOne(
		ctx,
		bson.M{"name": seed.Name},
		bson.M{"$set": bson.M{"name": seed.Name, "applied_at": time.Now().UTC()}},
		options.Update().SetUpsert(true),
	); err != nil {
		return fmt.Errorf("record applied: %w", err)
	}

	if err := seed.Run(ctx, db); err != nil {
		// Release an exclusive claim so the seed can be retried.
		if exclusive {
			_, _ = coll.DeleteOne(ctx, bson.M{"name": seed.Name})
		}
		return err
	}
	return nil
}
