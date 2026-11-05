// Package lock reads and reconciles stele.lock, which gives every rule a stable identity.
//
// stele.lock sits at the repository root and is committed. stele writes it and
// nobody edits it, like go.sum. It holds one entry per rule (id, source, body
// hash, position within the source), the repository's project id (D51), and
// the commit each pinned source read its rules from (D72).
package lock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/source"
)

// FileName is the lock's name at the repository root.
const FileName = "stele.lock"

// version is the lock format this stele writes when no source is pinned,
// and pinnedVersion the one it writes when one is (D72): a stele that reads
// only version 1 then refuses the lock instead of dropping its pins at the
// next compile. This stele reads both.
const (
	version       = 1
	pinnedVersion = 2
)

// maxSize caps stele.lock. An entry is about 150 bytes.
const maxSize = 16 << 20

var (
	// ErrDuplicateID is two rules sharing an id: a merge that duplicated an
	// entry, or two rules whose source supplied the same id.
	ErrDuplicateID = errors.New("duplicate rule id")
	// ErrInvalid is a stele.lock stele cannot use: not JSON, another version,
	// no project, or an entry with no id. A bad merge or a hand edit.
	ErrInvalid = errors.New("invalid")
)

// Entry is one rule's identity.
type Entry struct {
	ID       string `json:"id"`
	Source   string `json:"source"`   // the source the rule came from
	Hash     string `json:"hash"`     // source.Hash of the body
	Position int    `json:"position"` // within that source
}

// File is the content of stele.lock.
type File struct {
	Version int          `json:"version"`
	Project string       `json:"project"`           // this repository's UUID, minted once (D51)
	Sources []source.Pin `json:"sources,omitempty"` // the pinned sources' commits, in stele.json order (D72)
	Rules   []Entry      `json:"rules"`
}

// Load reads stele.lock under root. No stele.lock is an empty File, not an
// error: the first compile creates it. A lock that fails Validate is an error.
func Load(root string) (File, error) {
	f, err := load(root)
	// The lock is committed and holds every rule's identity, so it is never
	// regenerated silently: every refusal says how to recover.
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDuplicateID) {
		err = fmt.Errorf("%w; %s", err, recovery)
	}
	return f, err
}

// recovery is what to do about a stele.lock stele cannot use.
const recovery = "restore the committed stele.lock, or delete it and run stele compile (rules then get new ids)"

func load(root string) (File, error) {
	data, err := safefile.ReadRegular(filepath.Join(root, FileName), maxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if errors.Is(err, safefile.ErrNotRegular) {
		return File{}, fmt.Errorf("%s: %w: not a plain file (a symlink?)", FileName, ErrInvalid)
	}
	if errors.Is(err, safefile.ErrTooLarge) {
		return File{}, fmt.Errorf("%s: %w: over %d bytes", FileName, ErrInvalid, maxSize)
	}
	if err != nil {
		return File{}, err
	}
	return Decode(data)
}

// Decode reads the content of a stele.lock and validates it, as Load does. The
// git source uses it for the lock beside a shared stele.md (D72).
func Decode(data []byte) (File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w: %v", FileName, ErrInvalid, err)
	}
	if f.Version != version && f.Version != pinnedVersion {
		return File{}, fmt.Errorf("%s: %w: version %d, this stele reads versions %d and %d", FileName, ErrInvalid, f.Version, version, pinnedVersion)
	}
	if err := Validate(f); err != nil {
		return File{}, err
	}
	return f, nil
}

// commitID is a git commit id: SHA-1 or SHA-256, in lower-case hex.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Validate returns ErrDuplicateID when two entries share an id. Reconcile
// never produces one, but a merge can. It also rejects a lock with no project
// or an entry with no id, which only a hand edit can produce: accepting them
// would mint a second project id or let one rule claim two entries.
func Validate(f File) error {
	if f.Project == "" {
		return fmt.Errorf("%s: %w: no project id", FileName, ErrInvalid)
	}
	seen := make(map[string]int, len(f.Rules))
	for i, e := range f.Rules {
		if e.ID == "" {
			return fmt.Errorf("%s: %w: rule %d (%s position %d) has no id", FileName, ErrInvalid, i+1, e.Source, e.Position)
		}
		if j, ok := seen[e.ID]; ok {
			return fmt.Errorf("%s: %w %s: rules %d and %d (%s position %d, %s position %d)",
				FileName, ErrDuplicateID, e.ID, j+1, i+1,
				f.Rules[j].Source, f.Rules[j].Position, e.Source, e.Position)
		}
		seen[e.ID] = i
	}
	pinned := make(map[string]bool, len(f.Sources))
	for i, p := range f.Sources {
		switch {
		case p.Source == "":
			return fmt.Errorf("%s: %w: pinned source %d has no name", FileName, ErrInvalid, i+1)
		case !commitID.MatchString(p.Commit):
			return fmt.Errorf("%s: %w: pinned source %s: commit %q is not a git commit id", FileName, ErrInvalid, p.Source, p.Commit)
		case pinned[p.Source]:
			return fmt.Errorf("%s: %w: source %s is pinned twice", FileName, ErrInvalid, p.Source)
		}
		pinned[p.Source] = true
	}
	return nil
}

// Equal reports whether a and b would be saved as the same stele.lock. A lock
// on disk that is not Equal to the one compile plans is out of date.
func Equal(a, b File) bool {
	x, errX := encode(a)
	y, errY := encode(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

// Save writes stele.lock under root. The old lock is never truncated: the new
// content goes to a temp file in the same directory, which then replaces it.
func Save(root string, f File) error {
	data, err := encode(f)
	if err != nil {
		return err
	}
	return safefile.Replace(filepath.Join(root, FileName), data)
}

// encode is the bytes Save writes. An empty lock has "rules": [], not null.
// The version follows the pins.
func encode(f File) ([]byte, error) {
	if f.Rules == nil {
		f.Rules = []Entry{}
	}
	f.Version = version
	if len(f.Sources) > 0 {
		f.Version = pinnedVersion
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
