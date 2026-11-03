package safefile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendCreatesThenAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	rotated := filepath.Join(dir, "log.1.jsonl")
	for _, line := range []string{"one\n", "two\n"} {
		if err := Append(path, rotated, []byte(line), 1<<20, time.Second); err != nil {
			t.Fatalf("Append(%q): %v", line, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("content = %q, want both lines in order", got)
	}
	if _, err := os.Lstat(path + ".lock"); err == nil {
		t.Error("the lock file was left behind")
	}
}

// A directory where the log belongs is never stele's: the line is dropped and
// the directory left alone.
func TestAppendRefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Append(path, filepath.Join(dir, "log.1.jsonl"), []byte("x\n"), 1<<20, time.Second)
	if !errors.Is(err, ErrNotRegular) {
		t.Errorf("err = %v, want ErrNotRegular", err)
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		t.Error("the directory was moved or removed")
	}
}

// A symlink where the log belongs could point anywhere: stele never writes
// through it.
func TestAppendRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "log.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	err := Append(path, filepath.Join(dir, "log.1.jsonl"), []byte("x\n"), 1<<20, time.Second)
	if !errors.Is(err, ErrNotRegular) {
		t.Errorf("err = %v, want ErrNotRegular", err)
	}
	if got, _ := os.ReadFile(target); len(got) != 0 {
		t.Errorf("the link's target got %q", got)
	}
}

// A lock held by another writer makes Append give up after its wait, not hang.
func TestAppendGivesUpOnHeldLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	unlock, err := Lock(path+".lock", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	err = Append(path, filepath.Join(dir, "log.1.jsonl"), []byte("x\n"), 1<<20, 50*time.Millisecond)
	if err == nil {
		t.Fatal("Append wrote while the lock was held")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Append waited %v, want about its 50ms wait", d)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("the log was created while the lock was held")
	}
}

// Past the cap the log moves to the rotated name, replacing an older one, and
// a new log starts with the line.
func TestAppendRotatesPastMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	rotated := filepath.Join(dir, "log.1.jsonl")
	if err := os.WriteFile(rotated, []byte("oldest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const max = 10
	for _, line := range []string{"aaaa\n", "bbbb\n", "cccc\n"} {
		if err := Append(path, rotated, []byte(line), max, time.Second); err != nil {
			t.Fatalf("Append(%q): %v", line, err)
		}
	}
	got, _ := os.ReadFile(path)
	old, _ := os.ReadFile(rotated)
	if string(old) != "aaaa\nbbbb\n" || string(got) != "cccc\n" {
		t.Errorf("rotated = %q, log = %q; want %q and %q", old, got, "aaaa\nbbbb\n", "cccc\n")
	}
}

// Writers appending at once never interleave or lose a line.
func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	rotated := filepath.Join(dir, "log.1.jsonl")
	const writers, each = 20, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				line := fmt.Sprintf("%s %02d %02d\n", strings.Repeat("x", 100), w, i)
				if err := Append(path, rotated, []byte(line), 1<<20, 10*time.Second); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != writers*each {
		t.Fatalf("%d lines, want %d", len(lines), writers*each)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if len(l) != 106 || seen[string(l)] {
			t.Fatalf("torn or repeated line %q", l)
		}
		seen[string(l)] = true
	}
}
