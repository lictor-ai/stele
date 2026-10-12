// Package sign lets stele trust only the generated files it wrote on this
// machine.
//
// Nothing under .stele/ is covered by the repository's .gitignore, so a
// repository can commit an index.json whose fingerprint matches stele.md and
// whose rules do not, and the agent would be served rules nobody reviewed.
// Generated files therefore carry an HMAC-SHA256 made with a key that lives
// in the user's config directory, outside every repository. A committed file
// never verifies on another machine, so stele rebuilds it (#27).
//
// stele.lock is shared on purpose and is not signed: it holds no rule text,
// and changes to it show in review.
//
// This does not defend against malware already running as the user, which
// can read the key; nothing file-based can.
package sign

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lictor-ai/stele/internal/safefile"
)

const (
	keyFile = "key"
	keySize = 32
	// header starts every signed file, followed by the hex MAC and a newline.
	header = "stele-hmac-sha256 "
)

// ErrBadSeal is a file that was not signed with this machine's key: unsigned,
// signed elsewhere, edited, or signed for another purpose.
var ErrBadSeal = errors.New("not signed by stele on this machine")

// Key signs and verifies generated files.
type Key []byte

// DefaultDir is where the key lives: the user's config directory, never a
// repository.
func DefaultDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no user config directory for stele's key: %w", err)
	}
	return filepath.Join(dir, "stele"), nil
}

// LoadKey returns the key in dir, creating dir and the key on first use. The
// key file is owner-only. Two processes creating it at once agree on one key:
// the file appears whole, and the loser reads the winner's. A key that cannot
// be read is an error, never a reason to trust unsigned files.
func LoadKey(dir string) (Key, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("stele key directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, keyFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		k := make([]byte, keySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		err := safefile.Create(path, []byte(hex.EncodeToString(k)+"\n"), 0o600)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("stele key %s: %w", path, err)
		}
	}
	data, err := safefile.ReadRegular(path, 1024)
	if err != nil {
		return nil, fmt.Errorf("stele key %s: %w", path, err)
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(k) != keySize {
		return nil, fmt.Errorf("stele key %s is corrupt: delete it, and stele makes a new one and rebuilds what it signed", path)
	}
	return Key(k), nil
}

// Seal returns body signed for purpose. The purpose, such as "index" or
// "session:<id>", is part of what is signed, so a signed file cannot be
// passed off as another kind, or as another session's.
func (k Key) Seal(purpose string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	b.WriteString(hex.EncodeToString(k.mac(purpose, body)))
	b.WriteByte('\n')
	b.Write(body)
	return b.Bytes()
}

// Open verifies sealed for purpose and returns its body, or ErrBadSeal. A
// nil key verifies nothing.
func (k Key) Open(purpose string, sealed []byte) ([]byte, error) {
	if len(k) == 0 {
		return nil, ErrBadSeal
	}
	line, body, ok := bytes.Cut(sealed, []byte("\n"))
	if !ok {
		return nil, ErrBadSeal
	}
	got, ok := bytes.CutPrefix(line, []byte(header))
	if !ok {
		return nil, ErrBadSeal
	}
	mac, err := hex.DecodeString(string(bytes.TrimSpace(got)))
	if err != nil || !hmac.Equal(mac, k.mac(purpose, body)) {
		return nil, ErrBadSeal
	}
	return body, nil
}

// mac is HMAC-SHA256 over the purpose, a NUL, and the body.
func (k Key) mac(purpose string, body []byte) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}
