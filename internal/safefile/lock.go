package safefile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Lock takes the lock file name, waiting up to wait for another holder. A
// lock file older than stale was left by a crashed process and is taken over.
//
// The lock file holds a token unique to this holder, and unlock removes the
// file only while it still holds that token. So a holder that was taken over
// never removes its successor's lock.
//
// Creating a file exclusively works alike on every platform, which is why
// this is a lock file rather than flock or LockFileEx.
func Lock(name string, wait, stale time.Duration) (unlock func(), err error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	backoff := time.Millisecond
	for {
		err := create(name, token)
		if err == nil {
			return func() { release(name, token) }, nil
		}
		switch {
		case errors.Is(err, fs.ErrExist):
			if info, serr := os.Stat(name); serr == nil && time.Since(info.ModTime()) > stale {
				takeOver(name, token, stale)
				continue
			}
		case errors.Is(err, fs.ErrPermission) && runtime.GOOS == "windows":
			// A lock file just removed while another process still has it
			// open is "delete pending" on Windows, and creating it fails
			// with access denied until that handle closes.
		default:
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is held by another stele process: %w", filepath.Base(name), err)
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

// create makes the lock file exclusively, holding token.
func create(name, token string) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(token)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

// release removes the lock file if it still holds token.
func release(name, token string) {
	if data, err := os.ReadFile(name); err == nil && string(data) == token {
		os.Remove(name)
	}
}

// takeOver moves a stale lock out of the way. A rename succeeds for only one
// of several processes racing to take the same lock over, so only one of them
// removes it. If what it moved turns out to be fresh, another process had
// already taken over, and the lock is linked back.
func takeOver(name, token string, stale time.Duration) {
	grave := name + "." + token + ".stale"
	if err := os.Rename(name, grave); err != nil {
		return
	}
	if info, err := os.Stat(grave); err == nil && time.Since(info.ModTime()) <= stale {
		os.Link(grave, name) // fails if a new lock already exists; then both are live, the narrowest case
	}
	os.Remove(grave)
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
