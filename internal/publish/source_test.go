package publish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
)

// gitT runs plain git in dir for a test's own setup, with the hermetic
// config from TestMain.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newProject makes a git repository holding a committed stele.md and a
// stele.lock with project id p-1 in dir/sub, and returns the project root.
func newProject(t *testing.T, sub, rules string) string {
	t.Helper()
	testGit(t)
	top := t.TempDir()
	gitT(t, top, "init", "--quiet")
	root := filepath.Join(top, filepath.FromSlash(sub))
	writeFile(t, filepath.Join(root, "stele.md"), rules)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lock.Save(root, lock.File{Version: 1, Project: "p-1"}); err != nil {
		t.Fatal(err)
	}
	gitT(t, top, "add", "-A")
	gitT(t, top, "commit", "--quiet", "-m", "rules")
	return root
}

func TestReadCommitted(t *testing.T) {
	g := testGit(t)
	root := newProject(t, ".", "Use tabs.\n")
	c, err := readCommitted(context.Background(), g, root)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Blob) != "Use tabs.\n" {
		t.Errorf("blob = %q", c.Blob)
	}
	if c.Head != gitT(t, root, "rev-parse", "HEAD") {
		t.Errorf("head = %q", c.Head)
	}
	if c.Name != "t" || c.Email != "t@example.com" || c.Project != "p-1" {
		t.Errorf("committed = %+v", c)
	}
}

func TestReadCommittedSubdirectory(t *testing.T) {
	g := testGit(t)
	root := newProject(t, "svc", "Sub rules.\n")
	c, err := readCommitted(context.Background(), g, root)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Blob) != "Sub rules.\n" {
		t.Errorf("blob = %q", c.Blob)
	}
}

func TestReadCommittedCRLF(t *testing.T) {
	g := testGit(t)
	top := t.TempDir()
	gitT(t, top, "init", "--quiet")
	gitT(t, top, "config", "core.autocrlf", "true")
	writeFile(t, filepath.Join(top, "stele.md"), "a\r\nb\r\n")
	if err := lock.Save(top, lock.File{Version: 1, Project: "p-1"}); err != nil {
		t.Fatal(err)
	}
	gitT(t, top, "add", "-A")
	gitT(t, top, "commit", "--quiet", "-m", "rules")
	c, err := readCommitted(context.Background(), g, top)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Blob) != "a\nb\n" {
		t.Errorf("blob = %q, want the committed LF bytes", c.Blob)
	}
}

func TestReadCommittedRefusals(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()

	t.Run("not a repository", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "stele.md"), "x\n")
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		if _, err := readCommitted(ctx, g, dir); !errors.Is(err, ErrNotRepo) {
			t.Errorf("err = %v, want ErrNotRepo", err)
		}
	})
	t.Run("no commit yet", func(t *testing.T) {
		dir := t.TempDir()
		gitT(t, dir, "init", "--quiet")
		writeFile(t, filepath.Join(dir, "stele.md"), "x\n")
		gitT(t, dir, "add", "stele.md")
		_, err := readCommitted(ctx, g, dir)
		if !errors.Is(err, ErrUntracked) || !strings.Contains(err.Error(), "no commit") {
			t.Errorf("err = %v, want ErrUntracked saying there is no commit", err)
		}
	})
	t.Run("untracked", func(t *testing.T) {
		root := newProject(t, ".", "x\n")
		other := filepath.Join(root, "sub")
		writeFile(t, filepath.Join(other, "stele.md"), "y\n")
		_, err := readCommitted(ctx, g, other)
		if !errors.Is(err, ErrUntracked) || !strings.Contains(err.Error(), "HEAD") {
			t.Errorf("err = %v, want ErrUntracked", err)
		}
	})
	t.Run("staged, not committed", func(t *testing.T) {
		root := newProject(t, ".", "x\n")
		writeFile(t, filepath.Join(root, "stele.md"), "y\n")
		gitT(t, root, "add", "stele.md")
		if _, err := readCommitted(ctx, g, root); !errors.Is(err, ErrEdited) {
			t.Errorf("err = %v, want ErrEdited", err)
		}
	})
	t.Run("edited", func(t *testing.T) {
		root := newProject(t, ".", "x\n")
		writeFile(t, filepath.Join(root, "stele.md"), "y\n")
		if _, err := readCommitted(ctx, g, root); !errors.Is(err, ErrEdited) {
			t.Errorf("err = %v, want ErrEdited", err)
		}
	})
	t.Run("no stele.lock", func(t *testing.T) {
		root := newProject(t, ".", "x\n")
		gitT(t, root, "rm", "--quiet", "stele.lock")
		gitT(t, root, "commit", "--quiet", "-m", "no lock")
		_, err := readCommitted(ctx, g, root)
		if !errors.Is(err, ErrNoProject) || !strings.Contains(err.Error(), "stele compile") {
			t.Errorf("err = %v, want ErrNoProject", err)
		}
	})
	t.Run("invalid stele.lock", func(t *testing.T) {
		root := newProject(t, ".", "x\n")
		writeFile(t, filepath.Join(root, "stele.lock"), "{")
		if _, err := readCommitted(ctx, g, root); !errors.Is(err, lock.ErrInvalid) {
			t.Errorf("err = %v, want lock.ErrInvalid", err)
		}
	})
	t.Run("committed symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		dir := t.TempDir()
		gitT(t, dir, "init", "--quiet")
		writeFile(t, filepath.Join(dir, "real.md"), "x\n")
		if err := os.Symlink("real.md", filepath.Join(dir, "stele.md")); err != nil {
			t.Fatal(err)
		}
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "--quiet", "-m", "link")
		if _, err := readCommitted(ctx, g, dir); !errors.Is(err, ErrUntracked) {
			t.Errorf("err = %v, want ErrUntracked", err)
		}
	})
}
