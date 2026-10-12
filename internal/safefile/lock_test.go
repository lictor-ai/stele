package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitignore")
	if err := Create(path, []byte("*\n")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Create(path, []byte("other\n")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second Create: err = %v, want ErrExist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "*\n" {
		t.Errorf("content = %q, want the first write kept", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries, want only the file: a temp file was left", len(entries))
	}
}

func TestDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, ".stele")
	for range 2 { // creates, then accepts the existing directory
		if err := Dir(dir); err != nil {
			t.Fatalf("Dir: %v", err)
		}
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Dir(file); err == nil {
		t.Error("Dir accepted a file")
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	if err := Dir(link); err == nil {
		t.Error("Dir accepted a symlink to a directory")
	}
}

func TestLockExcludes(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.lock")
	const n = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		holders int
		maxSeen int
		errs    = make(chan error, n)
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := Lock(name, 10*time.Second, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			holders++
			maxSeen = max(maxSeen, holders)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			holders--
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Lock: %v", err)
	}
	if maxSeen != 1 {
		t.Errorf("%d holders at once, want 1", maxSeen)
	}
}

func TestLockTimesOut(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(name, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Lock(name, 50*time.Millisecond, time.Minute); err == nil {
		t.Error("second Lock succeeded while the first is held")
	}
}

// A stale lock is taken over, and the old holder's unlock then leaves the new
// holder's lock alone.
func TestLockStaleTakeover(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.lock")
	oldUnlock, err := Lock(name, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(name, past, past); err != nil {
		t.Fatal(err)
	}

	newUnlock, err := Lock(name, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("taking over a stale lock: %v", err)
	}
	oldUnlock() // the crashed holder wakes up
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("the old holder's unlock removed the new lock: %v", err)
	}
	newUnlock()
	if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file after unlock: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(name))
	if len(entries) != 0 {
		t.Errorf("%d files left behind", len(entries))
	}
}

// A lock that is not a plain file was committed by a cloned repository, not
// made by stele, and is moved aside at once whatever its age.
func TestLockTakesOverNonFile(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "x.lock")
		if err := os.MkdirAll(filepath.Join(name, "inside"), 0o755); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		unlock, err := Lock(name, time.Second, time.Hour)
		if err != nil {
			t.Fatalf("Lock over a directory: %v", err)
		}
		defer unlock()
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("took %v, want no wait", d)
		}
	})
	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		name := filepath.Join(dir, "x.lock")
		if err := os.Symlink(filepath.Join(dir, "nowhere"), name); err != nil {
			t.Skipf("cannot make a symlink here: %v", err)
		}
		unlock, err := Lock(name, time.Second, time.Hour)
		if err != nil {
			t.Fatalf("Lock over a dangling symlink: %v", err)
		}
		unlock()
	})
}

func TestLockMissingDir(t *testing.T) {
	name := filepath.Join(t.TempDir(), "missing", "x.lock")
	if _, err := Lock(name, time.Second, time.Hour); err == nil {
		t.Error("Lock in a missing directory succeeded")
	}
}

// A directory where the file belongs was committed by a clone, not written
// by stele: Replace moves it aside at once and writes a real file.
func TestReplaceOntoDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := Replace(path, []byte("x")); err != nil {
		t.Fatalf("Replace onto a directory: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("took %v, want no wait", d)
	}
	got, err := ReadRegular(path, 10)
	if err != nil || string(got) != "x" {
		t.Errorf("after Replace: %q, %v; want a plain file holding x", got, err)
	}
}

// A forced takeover, for a lock that looked like no lock at all, must not
// remove a live lock that a racing process created in its place.
func TestForcedTakeOverKeepsLiveLock(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(name, time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if takeOver(name, "racer", time.Hour, true) {
		t.Error("forced takeover of a live lock reported success")
	}
	if info, err := os.Lstat(name); err != nil || !info.Mode().IsRegular() {
		t.Errorf("live lock gone after a forced takeover: %v", err)
	}
}
