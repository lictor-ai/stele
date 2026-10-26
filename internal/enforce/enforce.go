// Package enforce holds the enforcement policy.
//
// The policy lives in stele.json at the repository root, beside stele.md. The
// file is optional and committed: the policy is a team decision, reviewed like
// code. stele reads it and never writes it (D38). The same file will also list
// plugins; Load reads only the policy keys and ignores the rest.
package enforce

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the policy file at the repository root.
const FileName = "stele.json"

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
	data, err := os.ReadFile(filepath.Join(root, FileName))
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
	data, err := os.ReadFile(filepath.Join(root, FileName))
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
