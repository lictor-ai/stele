package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/sign"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func TestLoadMissing(t *testing.T) {
	got, err := Load(t.TempDir(), "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(State{SessionID: "s1"}, got); diff != "" {
		t.Errorf("new session (-want +got):\n%s", diff)
	}
}

func TestUpdate(t *testing.T) {
	root := t.TempDir()
	st, err := Update(root, "s1", t0, testKey, func(s *State) error {
		s.PromptID = "p1"
		s.Armed = true
		s.Touched = []string{"internal/db"}
		s.Blocks = 1
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	want := State{SessionID: "s1", PromptID: "p1", Armed: true, Touched: []string{"internal/db"}, Blocks: 1, UpdatedAt: t0}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("returned state (-want +got):\n%s", diff)
	}
	got, err := Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("loaded state (-want +got):\n%s", diff)
	}

	// The other process changes another field; the first one's survive.
	if _, err := Update(root, "s1", t0, testKey, func(s *State) error {
		s.LastError = "request: no index"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err = Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	want.LastError = "request: no index"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("after a second update (-want +got):\n%s", diff)
	}

	// .stele/ is ignored, and no lock file is left behind.
	if _, err := os.Stat(filepath.Join(root, ".stele", ".gitignore")); err != nil {
		t.Errorf(".stele/.gitignore: %v", err)
	}
	if _, err := os.Stat(path(root, "s1") + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file left behind: %v", err)
	}
}

func TestUpdateErrorSavesNothing(t *testing.T) {
	root := t.TempDir()
	boom := errors.New("boom")
	_, err := Update(root, "s1", t0, testKey, func(s *State) error {
		s.Armed = true
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	got, err := Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Armed {
		t.Error("state saved although fn failed")
	}
}

// Two sessions on one repository never affect each other (D37).
func TestSessionsAreSeparate(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if _, err := Update(root, id, t0, testKey, func(s *State) error {
			s.Armed = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// a's fetch disarms a only.
	if _, err := Update(root, "a", t0, testKey, func(s *State) error {
		s.Armed = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := Load(root, "b", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Armed {
		t.Error("disarming session a disarmed session b")
	}
}

// Concurrent read-modify-write updates lose nothing.
func TestUpdateConcurrent(t *testing.T) {
	root := t.TempDir()
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Update(root, "s1", t0, testKey, func(s *State) error {
				s.Blocks++
				return nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	got, err := Load(root, "s1", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Blocks != n {
		t.Errorf("Blocks = %d, want %d: an update was lost", got.Blocks, n)
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// A crashed process left its lock file.
	lockFile := path(root, "s1") + ".lock"
	if err := os.WriteFile(lockFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Errorf("Update with a stale lock: %v", err)
	}
}

func TestCleanup(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"old", "recent"} {
		if _, err := Update(root, id, t0, testKey, func(*State) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	age := map[string]time.Duration{"old": 25 * time.Hour, "recent": time.Hour}
	for id, d := range age {
		ts := now.Add(-d)
		if err := os.Chtimes(path(root, id), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Update(root, "writer", now, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path(root, "old")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("session older than 24 hours kept: %v", err)
	}
	if _, err := os.Stat(path(root, "recent")); err != nil {
		t.Errorf("recent session removed: %v", err)
	}
}

func TestCurrent(t *testing.T) {
	root := t.TempDir()
	got, err := Current(root)
	if err != nil || got != "" {
		t.Fatalf("Current with none = %q, %v; want \"\", nil", got, err)
	}
	for _, id := range []string{"s1", "s2"} {
		if err := SetCurrent(root, id); err != nil {
			t.Fatal(err)
		}
		got, err := Current(root)
		if err != nil {
			t.Fatal(err)
		}
		if got != id {
			t.Errorf("Current = %q, want %q", got, id)
		}
	}
}

// The id comes from the host agent and names a file, so it cannot escape
// .stele/state.
func TestBadSessionID(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", "../escape", `a\b`, "a/b", "..", "a.b", "CON", "nul", "Com1", "LPT9"} {
		if _, err := Load(root, id, testKey); !errors.Is(err, ErrBadSessionID) {
			t.Errorf("Load(%q): err = %v, want ErrBadSessionID", id, err)
		}
		if _, err := Update(root, id, t0, testKey, func(*State) error { return nil }); !errors.Is(err, ErrBadSessionID) {
			t.Errorf("Update(%q): err = %v, want ErrBadSessionID", id, err)
		}
		if err := SetCurrent(root, id); !errors.Is(err, ErrBadSessionID) {
			t.Errorf("SetCurrent(%q): err = %v, want ErrBadSessionID", id, err)
		}
	}
}

// A .stele/state committed as a symlink is refused: otherwise session files
// would be written, and old *.json files deleted, wherever it points.
func TestUpdateRefusesSymlinkedState(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "credentials.json")
	if err := os.WriteFile(victim, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".stele"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".stele", "state")); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	if _, err := Update(root, "s1", time.Now(), testKey, func(*State) error { return nil }); err == nil {
		t.Error("Update through a symlinked .stele/state succeeded")
	}
	if err := SetCurrent(root, "s1"); err == nil {
		t.Error("SetCurrent through a symlinked .stele/state succeeded")
	}
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) != 1 {
		t.Errorf("the symlink's target holds %d files, want only the untouched victim", len(entries))
	}
}

// Cleanup removes an old session's lock only if the lock is old too.
func TestCleanupKeepsFreshLock(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "old", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	past := now.Add(-25 * time.Hour)
	if err := os.Chtimes(path(root, "old"), past, past); err != nil {
		t.Fatal(err)
	}
	// An update of "old" is in progress and holds its lock.
	lockFile := path(root, "old") + ".lock"
	if err := os.WriteFile(lockFile, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(root, "writer", now, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Errorf("cleanup removed a fresh lock: %v", err)
	}
	// The lock is held, so the session is not idle and its file stays.
	if _, err := os.Stat(path(root, "old")); err != nil {
		t.Errorf("cleanup removed a session file whose lock is held: %v", err)
	}
}

// A state file that is not JSON fails Load, but Update starts the session
// afresh rather than failing every update for a day.
func TestUpdateRecoversUnreadableState(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(root, "s1"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "s1", testKey); err == nil {
		t.Error("Load of a corrupt file succeeded")
	}
	st, err := Update(root, "s1", t0, testKey, func(s *State) error {
		s.Armed = true
		return nil
	})
	if err != nil {
		t.Fatalf("Update over a corrupt file: %v", err)
	}
	if !st.Armed {
		t.Error("Update did not apply fn")
	}
	if _, err := Load(root, "s1", testKey); err != nil {
		t.Errorf("Load after recovery: %v", err)
	}
}

// A planted symlink to a device must not hang or exhaust memory.
func TestReadsRefuseSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/zero on Windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".stele", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", path(root, "s1")); err != nil {
		t.Fatal(err)
	}
	// A current that is not a plain file names no session.
	if id, err := Current(root); err != nil || id != "" {
		t.Errorf("Current through a symlink = %q, %v; want no session", id, err)
	}
	if _, err := Load(root, "s1", testKey); err == nil {
		t.Error("Load through a symlink succeeded")
	}
	// Update replaces the link with a real file instead of reading through it.
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Errorf("Update over a symlinked state file: %v", err)
	}
}

func TestLoadTooLarge(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(root, "s1"), make([]byte, maxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "s1", testKey); err == nil {
		t.Error("Load of an oversized file succeeded")
	}
}

// Leftovers from crashes are cleaned up once old: an orphaned lock, a temp
// file from Replace, a stale lock moved aside.
func TestCleanupLeftovers(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".stele", "state")
	now := time.Now()
	past := now.Add(-25 * time.Hour)
	leftovers := []string{"gone.json.lock", ".s1.json.123.tmp", "x.json.lock.abc.stale"}
	for _, name := range leftovers {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Update(root, "s1", now, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range leftovers {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", name, err)
		}
	}
}

// stele mcp reads state outside the lock while the hook updates it. On
// Windows a read that meets the rename must retry, not fail, and the rename
// must outlast the read.
func TestLoadDuringUpdates(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "s1", t0, testKey, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	readErrs := make(chan error, 1)
	go func() {
		defer close(readErrs)
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := Load(root, "s1", testKey); err != nil {
				readErrs <- err
				return
			}
		}
	}()
	for i := range 200 {
		if _, err := Update(root, "s1", t0, testKey, func(s *State) error {
			s.Blocks = i
			return nil
		}); err != nil {
			close(done)
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	close(done)
	if err := <-readErrs; err != nil {
		t.Errorf("Load during updates: %v", err)
	}
}

// A committed directory at current names no session, and SetCurrent moves it
// aside at once and records the session.
func TestCurrentNotAFile(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, ".stele", "state", "current")
	if err := os.MkdirAll(filepath.Join(cur, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := Current(root)
	if err != nil || id != "" {
		t.Errorf("Current = %q, %v; want no session", id, err)
	}
	start := time.Now()
	if err := SetCurrent(root, "s1"); err != nil {
		t.Fatalf("SetCurrent over a directory: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("SetCurrent took %v, want no wait", d)
	}
	if id, err := Current(root); err != nil || id != "s1" {
		t.Errorf("Current = %q, %v; want s1", id, err)
	}
}

var testKey = sign.Key(bytes.Repeat([]byte{7}, 32))

// A state file stele did not sign on this machine is never believed, and the
// session starts afresh: a repository cannot plant a disarmed state, and one
// session's file cannot pass for another's.
func TestPlantedStateIsIgnored(t *testing.T) {
	root := t.TempDir()
	if _, err := Update(root, "other", t0, testKey, func(s *State) error {
		s.Blocks = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	signedForOther, err := os.ReadFile(path(root, "other"))
	if err != nil {
		t.Fatal(err)
	}
	unsigned := []byte(`{"session_id": "s1", "armed": false, "blocks": 99}`)
	foreign := sign.Key(bytes.Repeat([]byte{8}, 32)).Seal("session:s1", unsigned)

	for name, content := range map[string][]byte{
		"unsigned":              unsigned,
		"another machine's key": foreign,
		"another session's":     signedForOther,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path(root, "s1"), content, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root, "s1", testKey); err == nil {
				t.Error("Load believed a file stele did not sign for this session")
			}
			st, err := Update(root, "s1", t0, testKey, func(*State) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if st.Blocks != 0 {
				t.Errorf("Blocks = %d, want a fresh state", st.Blocks)
			}
		})
	}
}

func TestUpdateNeedsKey(t *testing.T) {
	if _, err := Update(t.TempDir(), "s1", t0, nil, func(*State) error { return nil }); err == nil {
		t.Error("Update without a key succeeded")
	}
}
