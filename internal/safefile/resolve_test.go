package safefile

import (
	"os"
	"path/filepath"
	"strings"
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

// sameDir reports whether got is want itself, not a link to it: got is not
// followed. Text cannot tell, as a temp directory can be spelled with an 8.3
// short name on one side and the long name on the other.
func sameDir(t *testing.T, got, want string) bool {
	t.Helper()
	a, err := os.Lstat(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(a, b)
}

func TestResolveSymlink(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if sameDir(t, link, out) {
		t.Fatal("sameDir followed the link: it could not tell Resolve did nothing")
	}
	rest := filepath.Join("new", "c.txt")
	got, ok := Resolve(filepath.Join(link, rest))
	base, found := strings.CutSuffix(got, string(filepath.Separator)+rest)
	if !ok || !found || !sameDir(t, base, out) {
		t.Errorf("through a link = %q, %v, want %s below %s", got, ok, rest, out)
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
