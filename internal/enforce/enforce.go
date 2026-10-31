// Package enforce holds the enforcement policy.
//
// The policy lives in stele.json at the repository root, beside stele.md. The
// file is optional and committed: the policy is a team decision, reviewed like
// code. stele reads it and never writes it (D38). The same file will also list
// plugins; Load reads only the policy keys and ignores the rest.
package enforce

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the policy file at the repository root.
const FileName = "stele.json"

// maxSize caps stele.json. It is read on every write, by the hook.
const maxSize = 1 << 20

// Policy is how hard the hook enforces. Which parts of the hook run is not
// policy: stele init writes it on the hook command (ADR-0006, ADR-0008).
type Policy struct {
	// BlockOnError is whether a failure of stele itself blocks the write,
	// saying why, rather than letting it through with a warning. Default
	// false (ADR-0004).
	BlockOnError bool
}

// Default is the policy when stele.json is absent or does not set a key.
func Default() Policy { return Policy{} }

// Load reads the policy from stele.json under root. No stele.json is the
// default policy, not an error; that is the normal case. A stele.json that
// is not valid JSON is an error, returned with the default policy so a caller
// can still act on it.
func Load(root string) (Policy, error) {
	data, err := read(root)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}
	var raw struct {
		BlockOnError *bool `json:"block_on_error"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Default(), fmt.Errorf("%s: %w", FileName, err)
	}
	p := Default()
	if raw.BlockOnError != nil {
		p.BlockOnError = *raw.BlockOnError
	}
	return p, nil
}

// SetsBackstop reports whether stele.json under root sets "backstop", which
// nothing reads since ADR-0008: stele init says so, and never edits the file
// (D38). A missing or invalid stele.json sets nothing.
func SetsBackstop(root string) bool {
	data, err := read(root)
	if err != nil {
		return false
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(data, &keys) != nil {
		return false
	}
	_, ok := keys["backstop"]
	return ok
}

// read returns stele.json under root, without the byte order mark some
// Windows editors and shells add.
//
// A symlink is followed, since a user may share one policy between projects,
// but it must end at a regular file of bounded size: a repository could commit
// stele.json as a link to /dev/zero, and the hook reads it on every write.
func read(root string) ([]byte, error) {
	path := filepath.Join(root, FileName)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", FileName)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("%s is over %d bytes", FileName, maxSize)
	}
	return bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), nil
}
