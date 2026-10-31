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
	"io/fs"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/safefile"
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

// read returns stele.json under root as Text returns it.
//
// A symlink is followed, since a user may share one policy between projects,
// but it must end at a regular file of bounded size: a repository could commit
// stele.json as a link to /dev/zero, and the hook reads it on every write.
func read(root string) ([]byte, error) {
	data, err := safefile.ReadFollowing(filepath.Join(root, FileName), maxSize)
	if err != nil {
		return nil, err
	}
	return Text(data)
}

// Text returns the content of stele.json ready to parse: without the UTF-8
// byte order mark some Windows editors and shells add. UTF-16, which
// PowerShell 5.1's > writes, is not decoded: the error says what to do.
func Text(data []byte) ([]byte, error) {
	if bytes.HasPrefix(data, []byte("\xff\xfe")) || bytes.HasPrefix(data, []byte("\xfe\xff")) {
		return nil, fmt.Errorf("%s is UTF-16: save it as UTF-8", FileName)
	}
	return bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), nil
}
