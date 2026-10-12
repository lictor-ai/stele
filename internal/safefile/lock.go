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
		info, lerr := os.Lstat(name)
		switch {
		case lerr == nil && !info.Mode().IsRegular():
			// A directory, a symlink or a device is never a lock stele
			// made: a cloned repository committed it. Move it aside.
			if takeOver(name, token, stale, true) {
				continue
			}
		case lerr == nil && time.Since(info.ModTime()) > stale:
			if takeOver(name, token, stale, false) {
				continue
			}
		case lerr == nil:
			// Held by a live process: wait.
		case errors.Is(err, fs.ErrExist) && errors.Is(lerr, fs.ErrNotExist):
			// Removed by its holder between the two calls: try again.
			continue
		case errors.Is(err, fs.ErrExist), errors.Is(err, fs.ErrPermission) && runtime.GOOS == "windows":
			// It exists but cannot be inspected, or cannot be created: on
			// Windows a lock file just removed while another process still
			// has it open is "delete pending", and both creating and
			// inspecting it fail until that handle closes. Wait.
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

// takeOver moves a stale lock out of the way, and reports whether it did. A
// rename succeeds for only one of several processes racing to take the same
// lock over, so only one of them removes it. If what it moved turns out to be
// a fresh lock file, another process had already taken over, and the lock is
// linked back. That check runs even when force says the lock looked like no
// lock at all: between the look and the rename, a racing process may have
// moved the non-file aside and created a real lock. A non-empty directory
// moved aside stays, renamed.
func takeOver(name, token string, stale time.Duration, force bool) bool {
	grave := name + "." + token + ".stale"
	if err := os.Rename(name, grave); err != nil {
		return false
	}
	if info, err := os.Lstat(grave); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) <= stale {
		os.Link(grave, name) // fails if a new lock already exists; then both are live, the narrowest case
		os.Remove(grave)
		return !force // a forced takeover that moved a live lock took nothing over
	}
	os.Remove(grave)
	return true
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
