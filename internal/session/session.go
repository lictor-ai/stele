// Package session stores per-session state: which rules each agent has been
// shown this turn.
//
// Every hook call is a separate process, and the agent often writes several
// files at once, so the state is shared through files (spec §2.3):
// .stele/state/<session>.json, one per Claude Code session (D37). Two sessions
// on one repository each have their own file and never touch each other's.
//
// Parallel hook calls update the same record, so every update is
// read-modify-write under a lock file (architecture §3, §7). The file is
// signed with this machine's key (D57).
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

const (
	stateDir    = "state"
	currentFile = "current"
	// maxAge is how long a session file outlives its last update. Nothing
	// tells stele a session ended.
	maxAge = 24 * time.Hour
	// staleLock is the age past which a lock file is taken to be left by a
	// crashed process. A lock is held for milliseconds.
	staleLock = 10 * time.Second
	// maxSize caps a state file. A real one is a few hundred bytes; the cap
	// stops a planted file from exhausting memory.
	maxSize = 1 << 20
)

// lockWait is how long an update waits for another process's lock. The hook
// runs on every write, so it must not wait long. A test raises it.
var lockWait = 2 * time.Second

// ErrBadSessionID is a session id that cannot name a file. The id comes from
// the host agent, or from .stele/state/current, which a cloned repository can
// commit, and becomes part of a path, so it is checked.
var ErrBadSessionID = errors.New("bad session id")

// idChars is what a session id may hold. Claude Code's are UUIDs.
var idChars = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// reserved holds the Windows device names. CON.json is the console whatever
// the extension, and reading it blocks forever.
var reserved = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])$`)

func validID(id string) bool {
	return idChars.MatchString(id) && !reserved.MatchString(id)
}

// State is one session's shared state.
type State struct {
	SessionID string `json:"session_id"`
	PromptID  string `json:"prompt_id"` // the turn Shown belongs to
	// Shown records the rules shown to each agent this turn: agent id ("" for
	// the main agent) to rule id to when it was shown. A subagent has its own
	// context, so it has its own entry (ADR-0004).
	Shown map[string]map[string]time.Time `json:"shown,omitempty"`
	// Denied records, for each agent this turn, the files whose write was
	// denied and when it was last denied: agent id to root-relative file to
	// time. A later write to that file is the agent's retry after reading
	// the reason.
	Denied map[string]map[string]time.Time `json:"denied,omitempty"`
	// VersionWarned is the version mismatch the user was last warned of,
	// so the warning comes once a session.
	VersionWarned string `json:"version_warned,omitempty"`
	// RulesWarned marks the invalid rules the user was last told of, so the
	// warning comes once a session for each set of problems (#54).
	RulesWarned string    `json:"rules_warned,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// errUnreadable is a state file that exists but holds no usable state: not
// signed by stele on this machine, not JSON, not a regular file, or too
// large. Update starts that session afresh.
var errUnreadable = errors.New("unreadable session state")

// Load reads a session's state. A session with no file yet is a zero State
// carrying its id, not an error. It reads only a regular file of bounded
// size, so a planted symlink or device cannot hang it, and only one signed
// with key for this session (internal/sign), so a planted file is never
// believed.
func Load(root, id string, key sign.Key) (State, error) {
	if !validID(id) {
		return State{}, fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	sealed, err := safefile.ReadRegular(path(root, id), maxSize)
	if errors.Is(err, fs.ErrNotExist) {
		return State{SessionID: id}, nil
	}
	if errors.Is(err, safefile.ErrNotRegular) || errors.Is(err, safefile.ErrTooLarge) {
		return State{}, fmt.Errorf("session %s: %w: %v", id, errUnreadable, err)
	}
	if err != nil {
		return State{}, err
	}
	data, err := key.Open(purpose(id), sealed)
	if err != nil {
		return State{}, fmt.Errorf("session %s: %w: %v", id, errUnreadable, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("session %s: %w: %v", id, errUnreadable, err)
	}
	st.SessionID = id
	return st, nil
}

// Update applies fn to a session's state and saves it, under the session's
// lock file so the hook and stele mcp never lose each other's changes. now
// stamps UpdatedAt. If fn returns an error nothing is saved. A state file
// that cannot be read is replaced by a fresh state rather than failing every
// update of the session. Afterwards, session files older than 24 hours are
// removed.
func Update(root, id string, now time.Time, key sign.Key, fn func(*State) error) (State, error) {
	if len(key) == 0 {
		return State{}, errors.New("session: no key to sign with")
	}
	if !validID(id) {
		return State{}, fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	if err := index.EnsureDir(root); err != nil {
		return State{}, err
	}
	dir := filepath.Join(root, index.Dir, stateDir)
	if err := safefile.Dir(dir); err != nil {
		return State{}, err
	}

	unlock, err := safefile.Lock(path(root, id)+".lock", lockWait, staleLock)
	if err != nil {
		return State{}, err
	}
	defer unlock()

	st, err := Load(root, id, key)
	if errors.Is(err, errUnreadable) {
		st, err = State{SessionID: id}, nil
	}
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
	if err := safefile.Replace(path(root, id), key.Seal(purpose(id), append(data, '\n'))); err != nil {
		return State{}, err
	}
	cleanup(dir, id, now)
	return st, nil
}

// SetCurrent records id as the session that last blocked, for the team
// milestone's request to learn which session it serves. Unused in v1.
func SetCurrent(root, id string) error {
	if !validID(id) {
		return fmt.Errorf("%w %q", ErrBadSessionID, id)
	}
	if err := index.EnsureDir(root); err != nil {
		return err
	}
	dir := filepath.Join(root, index.Dir, stateDir)
	if err := safefile.Dir(dir); err != nil {
		return err
	}
	return safefile.Replace(filepath.Join(dir, currentFile), []byte(id+"\n"))
}

// Current returns the session that last blocked, or "" when none has. The
// file may have been committed by a cloned repository, so it is read like a
// state file and its id checked the same way.
func Current(root string) (string, error) {
	data, err := safefile.ReadRegular(filepath.Join(root, index.Dir, stateDir, currentFile), 1024)
	// Something stele did not write, such as a committed directory, names no
	// session; the hook's next SetCurrent says which one blocked.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, safefile.ErrNotRegular) || errors.Is(err, safefile.ErrTooLarge) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !validID(id) {
		return "", fmt.Errorf("%s: %w %q", currentFile, ErrBadSessionID, id)
	}
	return id, nil
}

// purpose is what a session's state is signed as, so one session's file
// cannot pass for another's.
func purpose(id string) string { return "session:" + id }

func path(root, id string) string {
	return filepath.Join(root, index.Dir, stateDir, id+".json")
}

// cleanup removes what has not changed for maxAge: session files, and the
// lock, .tmp and .stale files a crash can leave behind. It is best effort: a
// file it cannot remove is retried on the next update. keep is the session
// just written.
//
// A session file goes only under that session's lock, and only if it is
// still old once the lock is held, so an update that lands meanwhile is
// never deleted.
func cleanup(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		id, isState := strings.CutSuffix(name, ".json")
		switch {
		case isState && id != keep:
			removeIdle(filepath.Join(dir, name), now)
		case strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".stale"):
			// Checked again just before removing: on Windows the listing's
			// times can be stale, and a lock may have been taken since.
			p := filepath.Join(dir, name)
			if fresh, err := os.Lstat(p); err == nil && now.Sub(fresh.ModTime()) > maxAge {
				os.Remove(p)
			}
		}
	}
}

// removeIdle removes a session file under its lock, without waiting for it.
func removeIdle(file string, now time.Time) {
	unlock, err := safefile.Lock(file+".lock", 0, staleLock)
	if err != nil {
		return // in use: it is not idle
	}
	defer unlock()
	if info, err := os.Lstat(file); err == nil && now.Sub(info.ModTime()) > maxAge {
		os.Remove(file)
	}
}
