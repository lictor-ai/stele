package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// noHardLinks makes hard links fail as they do on FAT and exFAT.
func noHardLinks(t *testing.T, err error) {
	t.Helper()
	old := link
	link = func(_, _ string) error { return err }
	t.Cleanup(func() { link = old })
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestCreateWithoutHardLinks(t *testing.T) {
	noLink := errors.New("operation not permitted")

	t.Run("writes the file", func(t *testing.T) {
		noHardLinks(t, noLink)
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore")
		if err := Create(path, []byte("*\n"), 0o644); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "*\n" {
			t.Errorf("content = %q, %v", got, err)
		}
		if got := names(t, dir); len(got) != 1 {
			t.Errorf("directory holds %v, want the file alone", got)
		}
	})
	t.Run("never replaces a file", func(t *testing.T) {
		noHardLinks(t, noLink)
		path := filepath.Join(t.TempDir(), "stele.md")
		if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Create(path, []byte("other\n"), 0o644); !errors.Is(err, fs.ErrExist) {
			t.Errorf("err = %v, want ErrExist", err)
		}
		if got, _ := os.ReadFile(path); string(got) != "mine\n" {
			t.Errorf("content = %q, want the file untouched", got)
		}
	})
	t.Run("a link that found the file is not retried", func(t *testing.T) {
		noHardLinks(t, fmt.Errorf("link: %w", fs.ErrExist))
		path := filepath.Join(t.TempDir(), "x")
		if err := Create(path, []byte("x"), 0o644); !errors.Is(err, fs.ErrExist) {
			t.Errorf("err = %v, want ErrExist", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat = %v, want no file", err)
		}
	})
	// The signing key: an owner-only file has no such protection on a disk
	// with no hard links, and a racing process could read part of it.
	t.Run("an owner-only file is refused", func(t *testing.T) {
		noHardLinks(t, noLink)
		dir := t.TempDir()
		if err := Create(filepath.Join(dir, "key"), []byte("secret"), 0o600); !errors.Is(err, noLink) {
			t.Errorf("err = %v, want the link's error", err)
		}
		if got := names(t, dir); len(got) != 0 {
			t.Errorf("directory holds %v, want nothing", got)
		}
	})
	t.Run("one of many wins", func(t *testing.T) {
		noHardLinks(t, noLink)
		dir := t.TempDir()
		path := filepath.Join(dir, "x")
		const n = 8
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = Create(path, fmt.Appendf(nil, "writer %d\n", i), 0o644)
			}()
		}
		wg.Wait()
		won := -1
		for i, err := range errs {
			switch {
			case err == nil && won >= 0:
				t.Errorf("writers %d and %d both won", won, i)
			case err == nil:
				won = i
			case !errors.Is(err, fs.ErrExist):
				t.Errorf("writer %d: %v", i, err)
			}
		}
		if won < 0 {
			t.Fatal("no writer won")
		}
		if got, _ := os.ReadFile(path); string(got) != fmt.Sprintf("writer %d\n", won) {
			t.Errorf("content = %q, want writer %d's", got, won)
		}
		if got := names(t, dir); len(got) != 1 {
			t.Errorf("directory holds %v, want the file alone", got)
		}
	})
}
