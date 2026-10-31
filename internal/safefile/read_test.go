package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFollowing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFollowing(file, 5)
	if err != nil || string(got) != "12345" {
		t.Errorf("ReadFollowing = %q, %v; want the content", got, err)
	}
	if _, err := ReadFollowing(file, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: err = %v, want ErrTooLarge", err)
	}
	if _, err := ReadFollowing(filepath.Join(dir, "missing"), 5); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v, want ErrNotExist", err)
	}
	if _, err := ReadFollowing(dir, 5); !errors.Is(err, ErrNotRegular) {
		t.Errorf("directory: err = %v, want ErrNotRegular", err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	if got, err := ReadFollowing(link, 5); err != nil || string(got) != "12345" {
		t.Errorf("link to a file = %q, %v; want the content", got, err)
	}
	if _, err := ReadFollowing(link, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("link over the limit: err = %v, want ErrTooLarge", err)
	}
	toDir := filepath.Join(dir, "todir")
	if err := os.Symlink(dir, toDir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFollowing(toDir, 5); !errors.Is(err, ErrNotRegular) {
		t.Errorf("link to a directory: err = %v, want ErrNotRegular", err)
	}
}

func TestReadRegular(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRegular(file, 5)
	if err != nil || string(got) != "12345" {
		t.Errorf("ReadRegular = %q, %v; want the content", got, err)
	}
	if _, err := ReadRegular(file, 4); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: err = %v, want ErrTooLarge", err)
	}
	if _, err := ReadRegular(filepath.Join(dir, "missing"), 5); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v, want ErrNotExist", err)
	}
	if _, err := ReadRegular(dir, 5); !errors.Is(err, ErrNotRegular) {
		t.Errorf("directory: err = %v, want ErrNotRegular", err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	if _, err := ReadRegular(link, 5); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlink: err = %v, want ErrNotRegular", err)
	}
}
