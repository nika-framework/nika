package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// filePattern matches migration filenames such as:
//
//	20240521123045_create_users.up.sql
//	20240521123045_create_users.down.sql
var filePattern = regexp.MustCompile(`^([0-9]{6,})_([a-zA-Z0-9_\-]+)\.(up|down)\.sql$`)

// LoadFS discovers migrations under root inside the given fs.FS.
// Files must be named "<version>_<name>.up.sql" and, optionally,
// "<version>_<name>.down.sql". The returned migrations execute the SQL body
// verbatim; each file is treated as a single statement (add your own
// separators if needed).
func LoadFS(root fs.FS, dir string) ([]*Migration, error) {
	entries, err := fs.ReadDir(root, dir)
	if err != nil {
		return nil, fmt.Errorf("migration read dir: %w", err)
	}

	type pair struct {
		version int64
		name    string
		up      string
		down    string
	}
	byVersion := map[int64]*pair{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		match := filePattern.FindStringSubmatch(e.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration parse version %q: %w", match[1], err)
		}
		body, err := fs.ReadFile(root, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("migration read %s: %w", e.Name(), err)
		}
		text := strings.TrimSpace(string(body))

		p, ok := byVersion[version]
		if !ok {
			p = &pair{version: version, name: match[2]}
			byVersion[version] = p
		}
		if match[3] == "up" {
			p.up = text
		} else {
			p.down = text
		}
	}

	out := make([]*Migration, 0, len(byVersion))
	for _, p := range byVersion {
		if p.up == "" {
			return nil, fmt.Errorf("migration %d %q: missing .up.sql", p.version, p.name)
		}
		out = append(out, buildFileMigration(p.version, p.name, p.up, p.down))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// fileChecksum fingerprints the migration text so the runner can detect that an
// already-applied file was edited afterwards. Both directions are hashed: an
// edited .down.sql is just as much a divergence from what the database was built
// with.
func fileChecksum(up, down string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(up))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(down))
	return hex.EncodeToString(h.Sum(nil))
}

func buildFileMigration(version int64, name, up, down string) *Migration {
	mig := &Migration{
		Version:  version,
		Name:     name,
		Checksum: fileChecksum(up, down),
		Up: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, up)
			return err
		},
	}
	if down != "" {
		mig.Down = func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, down)
			return err
		}
	}
	return mig
}
