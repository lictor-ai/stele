package safefile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	for _, content := range []string{"first\n", "second\n"} {
		if err := Replace(path, []byte(content)); err != nil {
			t.Fatalf("Replace(%q): %v", content, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Errorf("content = %q, want %q", got, content)
		}
	}

	// No temp file is left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir holds %v, want only f.json", names)
	}
}

// stele's files are committed or read by other users and CI, so they must not
// end up with CreateTemp's 0600.
func TestReplaceMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "f.json")
	if err := Replace(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %v, want 0644", got)
	}
}

func TestReplaceMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "f.json")
	if err := Replace(path, []byte("x")); err == nil {
		t.Error("Replace into a missing directory succeeded, want an error")
	}
}
