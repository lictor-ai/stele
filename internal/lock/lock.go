// Package lock reads and reconciles stele.lock, which gives every rule a stable identity.
//
// stele.lock sits at the repository root and is committed. stele writes it and
// nobody edits it, like go.sum. It holds one entry per rule (id, source, body
// hash, position within the source) and the repository's project id (D51).
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileName is the lock's name at the repository root.
const FileName = "stele.lock"

// version is the lock format this stele reads and writes.
const version = 1

// ErrDuplicateID is two rules sharing an id: a merge that duplicated an entry,
// or two rules whose source supplied the same id.
var ErrDuplicateID = errors.New("duplicate rule id")

// Entry is one rule's identity.
type Entry struct {
	ID       string `json:"id"`
	Source   string `json:"source"`   // the source the rule came from
	Hash     string `json:"hash"`     // source.Hash of the body
	Position int    `json:"position"` // within that source
}

// File is the content of stele.lock.
type File struct {
	Version int     `json:"version"`
	Project string  `json:"project"` // this repository's UUID, minted once (D51)
	Rules   []Entry `json:"rules"`
}

// Load reads stele.lock under root. No stele.lock is an empty File, not an
// error: the first compile creates it. A lock that fails Validate is an error.
func Load(root string) (File, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if f.Version != version {
		return File{}, fmt.Errorf("%s: version %d, this stele reads version %d", FileName, f.Version, version)
	}
	if err := Validate(f); err != nil {
		return File{}, err
	}
	return f, nil
}

// Validate returns ErrDuplicateID when two entries share an id. Reconcile
// never produces one, but a merge can. It also rejects a lock with no project
// or an entry with no id, which only a hand edit can produce: accepting them
// would mint a second project id or let one rule claim two entries.
func Validate(f File) error {
	if f.Project == "" {
		return fmt.Errorf("%s: no project id", FileName)
	}
	seen := make(map[string]int, len(f.Rules))
	for i, e := range f.Rules {
		if e.ID == "" {
			return fmt.Errorf("%s: rule %d (%s position %d) has no id", FileName, i+1, e.Source, e.Position)
		}
		if j, ok := seen[e.ID]; ok {
			return fmt.Errorf("%s: %w %s: rules %d and %d (%s position %d, %s position %d)",
				FileName, ErrDuplicateID, e.ID, j+1, i+1,
				f.Rules[j].Source, f.Rules[j].Position, e.Source, e.Position)
		}
		seen[e.ID] = i
	}
	return nil
}

// Save writes stele.lock under root. The old lock is never truncated: the new
// content goes to a temp file in the same directory, which then replaces it.
func Save(root string, f File) error {
	if f.Rules == nil {
		f.Rules = []Entry{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return replaceFile(filepath.Join(root, FileName), data)
}

// replaceFile writes data to path through a temp file and a rename. On Windows
// the rename is refused while another process holds the target open, so it
// retries a few times before giving up.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	// CreateTemp makes the file 0600, and the rename would carry that over to
	// a committed file that other users and CI must read.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	backoff := 10 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp.Name(), path)
		if err == nil {
			return nil
		}
		if attempt == 4 {
			return fmt.Errorf("replace %s: %w (an editor, antivirus or search indexer may hold it open)",
				filepath.Base(path), err)
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}
