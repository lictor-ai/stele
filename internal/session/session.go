// Package session stores per-session state: arm state and touched directories.
//
// stele hook and stele mcp are separate processes, so they share a session
// through files (spec §2.3): .stele/state/<session>.json, one per Claude Code
// session (D37), and .stele/state/current, naming the session that last
// blocked. Two sessions on one repository each have their own file and never
// touch each other's.
//
// Both processes update the same record, so every update is read-modify-write
// under a lock file (architecture §3, §7).
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
)

const (
	stateDir    = "state"
	currentFile = "current"
	// maxAge is how long a session file outlives its last update. Nothing
	// tells stele a session ended.
	maxAge = 24 * time.Hour
	// lockWait is how long an update waits for another process's lock. The
	// hook runs on every write, so it must not wait long.
	lockWait = 2 * time.Second
	// staleLock is the age past which a lock file is taken to be left by a
	// crashed process. A lock is held for milliseconds.
	staleLock = 10 * time.Second
)

// ErrBadSessionID is a session id that cannot name a file. The id comes from
// the host agent and becomes part of a path, so it is checked.
var ErrBadSessionID = errors.New("bad session id")

// validID is what a session id may hold. Claude Code's are UUIDs.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// State is one session's shared state.
type State struct {
	SessionID string    `json:"session_id"`
	PromptID  string    `json:"prompt_id"`            // the current turn
	Armed     bool      `json:"armed"`                // the hook arms, fetch disarms
	Touched   []string  `json:"touched,omitempty"`    // directories written to this turn
	Blocks    int       `json:"blocks"`               // consecutive blocks with no fetch: the circuit breaker
	LastError string    `json:"last_error,omitempty"` // written by stele mcp when an exchange step fails
	UpdatedAt time.Time `json:"updated_at"`
}

// Load reads a session's state. A session with no file yet is a zero State
// carrying its id, not an error.
func Load(root, id string) (State, error) {
	if !validID.MatchString(id) {
		return State{}, fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	data, err := os.ReadFile(path(root, id))
	if errors.Is(err, fs.ErrNotExist) {
		return State{SessionID: id}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("session %s: %w", id, err)
	}
	st.SessionID = id
	return st, nil
}

// Update applies fn to a session's state and saves it, under the session's
// lock file so the hook and stele mcp never lose each other's changes. now
// stamps UpdatedAt. If fn returns an error nothing is saved. Afterwards,
// session files older than 24 hours are removed.
func Update(root, id string, now time.Time, fn func(*State) error) (State, error) {
	if !validID.MatchString(id) {
		return State{}, fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	if err := index.EnsureDir(root); err != nil {
		return State{}, err
	}
	dir := filepath.Join(root, index.Dir, stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return State{}, err
	}

	unlock, err := lock(path(root, id) + ".lock")
	if err != nil {
		return State{}, err
	}
	defer unlock()

	st, err := Load(root, id)
	if err != nil {
		return State{}, err
	}
	if err := fn(&st); err != nil {
		return State{}, err
	}
	st.SessionID = id
	st.UpdatedAt = now
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return State{}, err
	}
	if err := safefile.Replace(path(root, id), append(data, '\n')); err != nil {
		return State{}, err
	}
	cleanup(dir, id, now)
	return st, nil
}

// SetCurrent records id as the session that last blocked. The hook calls it on
// every block, and request reads it to learn which session it serves.
func SetCurrent(root, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	if err := index.EnsureDir(root); err != nil {
		return err
	}
	dir := filepath.Join(root, index.Dir, stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return safefile.Replace(filepath.Join(dir, currentFile), []byte(id+"\n"))
}

// Current returns the session that last blocked, or "" when none has.
func Current(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, index.Dir, stateDir, currentFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !validID.MatchString(id) {
		return "", fmt.Errorf("%s: %w %q", currentFile, ErrBadSessionID, id)
	}
	return id, nil
}

func path(root, id string) string {
	return filepath.Join(root, index.Dir, stateDir, id+".json")
}

// lock takes a lock file by creating it exclusively, which works alike on
// every platform. It waits up to lockWait for another holder, and takes over
// a lock older than staleLock, which a crashed process left behind.
func lock(name string) (unlock func(), err error) {
	deadline := time.Now().Add(lockWait)
	backoff := time.Millisecond
	for {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { os.Remove(name) }, nil
		}
		switch {
		case errors.Is(err, fs.ErrExist):
			if info, err := os.Stat(name); err == nil && time.Since(info.ModTime()) > staleLock {
				os.Remove(name)
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

// cleanup removes session files and their lock files that have not been
// updated for maxAge. It is best effort: a file it cannot remove is retried
// on the next update. keep is the session just written.
func cleanup(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || id == keep {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		os.Remove(filepath.Join(dir, name))
		os.Remove(filepath.Join(dir, name+".lock"))
	}
}
