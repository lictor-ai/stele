package publish

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeTool puts an executable named name in dir and returns its path. It is
// never run: LookPath only needs it to look executable.
func fakeTool(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveToolRefusesInsideProject(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	fakeTool(t, bin, "faketool")
	t.Setenv("PATH", bin)
	_, err := resolveTool("faketool", root)
	if !errors.Is(err, ErrToolRefused) {
		t.Fatalf("err = %v, want ErrToolRefused", err)
	}
	if !strings.Contains(filepath.ToSlash(err.Error()), filepath.ToSlash(bin)) {
		t.Errorf("err = %q, want it to name %s", err, bin)
	}
	if runtime.GOOS == "windows" {
		if _, err := resolveTool("faketool", strings.ToUpper(root)); !errors.Is(err, ErrToolRefused) {
			t.Errorf("root in another case: err = %v, want ErrToolRefused", err)
		}
	}
}

// stele.json can sit in a subfolder of a repository; a tool anywhere in that
// repository came with the clone too.
func TestResolveToolRefusesInsideEnclosingRepository(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, "svc", "api")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(top, "bin")
	fakeTool(t, bin, "faketool")
	t.Setenv("PATH", bin)
	if _, err := resolveTool("faketool", root); !errors.Is(err, ErrToolRefused) {
		t.Errorf("err = %v, want ErrToolRefused", err)
	}
}

func TestResolveToolOutsideProject(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	want := fakeTool(t, bin, "faketool")
	t.Setenv("PATH", bin)
	got, err := resolveTool("faketool", root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Errorf("resolveTool = %q, want %q", got, want)
	}
}

func TestResolveToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveTool("faketool", t.TempDir()); !errors.Is(err, ErrToolNotFound) {
		t.Errorf("err = %v, want ErrToolNotFound", err)
	}
}

func TestResolveToolRefusesRelativePath(t *testing.T) {
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(string) (string, error) { return "faketool", exec.ErrDot }
	if _, err := resolveTool("faketool", t.TempDir()); !errors.Is(err, ErrToolRefused) {
		t.Errorf("err = %v, want ErrToolRefused", err)
	}
}

func TestResolveToolRefusesLinkIntoProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	real := fakeTool(t, filepath.Join(root, "bin"), "faketool")
	bin := t.TempDir()
	if err := os.Symlink(real, filepath.Join(bin, "faketool")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err := resolveTool("faketool", root); !errors.Is(err, ErrToolRefused) {
		t.Errorf("err = %v, want ErrToolRefused", err)
	}
}
