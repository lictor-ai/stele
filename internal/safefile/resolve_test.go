package safefile

import (
	"os"
	"path/filepath"
	"testing"
)

// The junction cases need a process to make the junction, and nothing on the
// hook's path starts one: they are in internal/toolpath's tests.

func TestResolvePlainPath(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "a", "b.txt")
	if got, ok := Resolve(plain); !ok || got != plain {
		t.Errorf("Resolve(%s) = %q, %v", plain, got, ok)
	}
}

func TestResolveSymlink(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	real, err := filepath.EvalSymlinks(out)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Resolve(filepath.Join(link, "new", "c.txt"))
	if want := filepath.Join(real, "new", "c.txt"); !ok || got != want {
		t.Errorf("through a link = %q, %v, want %q", got, ok, want)
	}
}

func TestResolveSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if got, ok := Resolve(a); ok {
		t.Errorf("Resolve = %q, want it to give up", got)
	}
}
