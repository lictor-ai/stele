package safefile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dirLink makes link a junction to target on Windows, a symlink elsewhere.
func dirLink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Skipf("mklink /J: %v: %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
}

func TestResolve(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	plain := filepath.Join(dir, "a", "b.txt")
	if got, ok := Resolve(plain); !ok || got != plain {
		t.Errorf("Resolve(%s) = %q, %v", plain, got, ok)
	}
	link := filepath.Join(dir, "link")
	dirLink(t, out, link)
	got, ok := Resolve(filepath.Join(link, "new", "c.txt"))
	if want := filepath.Join(out, "new", "c.txt"); !ok || !strings.EqualFold(got, want) {
		t.Errorf("through a link = %q, %v, want %q", got, ok, want)
	}
}

func TestResolveDanglingLink(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "gone")
	link := filepath.Join(dir, "link")
	dirLink(t, gone, link)
	if got, ok := Resolve(link); !ok || !strings.EqualFold(got, gone) {
		t.Errorf("Resolve = %q, %v, want %q", got, ok, gone)
	}
}

func TestResolveLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	dirLink(t, b, a)
	dirLink(t, a, b)
	if got, ok := Resolve(a); ok {
		t.Errorf("Resolve = %q, want it to give up", got)
	}
}
