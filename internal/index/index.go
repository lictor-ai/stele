// Package index loads and saves .stele/index.json, the compiled corpus.
//
// Everything that needs rules at run time reads the index, never a rule file
// (D40). The index is generated and gitignored (D24), so its format carries no
// compatibility promise: a Version mismatch reads as no index, and the caller
// rebuilds it.
package index

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/safefile"
)

const (
	// Dir is stele's working directory under the root. All of it is generated.
	Dir      = ".stele"
	fileName = "index.json"
	// Version is the index format this stele reads and writes.
	Version = 1
)

// ErrNoIndex is an index that is missing, unreadable or of another version.
// Callers rebuild it.
var ErrNoIndex = errors.New("no index")

// Index is the compiled corpus.
type Index struct {
	Version int               `json:"version"`
	Project string            `json:"project"` // copied from stele.lock, so the hot path never opens the lock
	Sources map[string]string `json:"sources"` // source name → fingerprint at compile
	Rules   []Entry           `json:"rules"`
}

// Entry is one compiled rule.
type Entry struct {
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Position    int               `json:"position"` // within its source, so a recompile can carry it over
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Scope       []string          `json:"scope,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"` // every other key, checker settings among them (D46)
	Body        string            `json:"body"`
	BodyHash    string            `json:"body_hash"`
}

// Path is the index's path under root.
func Path(root string) string { return filepath.Join(root, Dir, fileName) }

// Load reads the index under root. A missing, unreadable or other-version
// index is ErrNoIndex. It never reports staleness: only a source can say
// whether it changed (architecture §3).
func Load(root string) (Index, error) {
	data, err := os.ReadFile(Path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return Index{}, ErrNoIndex
	}
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, fmt.Errorf("%w: %s: %v", ErrNoIndex, fileName, err)
	}
	if idx.Version != Version {
		return Index{}, fmt.Errorf("%w: %s is version %d, this stele reads version %d", ErrNoIndex, fileName, idx.Version, Version)
	}
	return idx, nil
}

// Save writes the index under root, creating .stele/ and its .gitignore when
// they are missing. The old index is never truncated (architecture §7).
func Save(root string, idx Index) error {
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Nothing in .stele/ is committed, so one "*" covers it and the
	// repository's own .gitignore is never touched (spec §2.3).
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := safefile.Replace(ignore, []byte("*\n")); err != nil {
			return err
		}
	}
	data, err := encode(idx)
	if err != nil {
		return err
	}
	return safefile.Replace(Path(root), data)
}

// Equal reports whether a and b would be saved as the same file.
func Equal(a, b Index) bool {
	x, errX := encode(a)
	y, errY := encode(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

// encode is the bytes Save writes. Version is always this stele's.
func encode(idx Index) ([]byte, error) {
	idx.Version = Version
	if idx.Sources == nil {
		idx.Sources = map[string]string{}
	}
	if idx.Rules == nil {
		idx.Rules = []Entry{}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
