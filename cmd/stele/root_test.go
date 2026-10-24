package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRoot(t *testing.T) {
	// repo/.git, repo/sub/stele.md, repo/sub/deep/, repo/other/
	repo := t.TempDir()
	for _, d := range []string{".git", "sub/deep", "other"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "stele.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, from, want string
	}{
		{name: "stele.md wins over .git", from: "sub/deep", want: "sub"},
		{name: "stele.md in the directory itself", from: "sub", want: "sub"},
		{name: "falls back to .git", from: "other", want: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findRoot(filepath.Join(repo, tt.from))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(repo, tt.want); got != want {
				t.Errorf("findRoot(%s) = %s, want %s", tt.from, got, want)
			}
		})
	}
}

// A .git, a directory or the file a worktree or submodule has, ends the
// search: rules never cross into another checkout (#43).
func TestFindRootStopsAtGit(t *testing.T) {
	// repo/.git/, repo/stele.md, repo/wt/.git (file), repo/wt/deep/,
	// repo/nested/.git/, repo/sub/stele.md, repo/sub/deep/
	repo := t.TempDir()
	for _, d := range []string{".git", "wt/deep", "nested/.git", "sub/deep"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"stele.md": "", "wt/.git": "gitdir: x\n", "sub/stele.md": ""} {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(name)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for from, want := range map[string]string{"wt/deep": "wt", "wt": "wt", "nested": "nested", "sub/deep": "sub", ".": "."} {
		got, err := findRoot(filepath.Join(repo, from))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(repo, want); got != want {
			t.Errorf("findRoot(%s) = %s, want %s", from, got, want)
		}
	}
}

// worktree adds a worktree of main at .claude/worktrees/<name> on a new
// branch from HEAD, where Claude Code puts an agent's.
func worktree(t *testing.T, main, name string) string {
	t.Helper()
	dir := filepath.Join(main, ".claude", "worktrees", name)
	gitT(t, main, "worktree", "add", "-q", "-b", name, dir)
	return dir
}

func TestFindRootInWorktree(t *testing.T) {
	main := gitProject(t, "")
	wt := worktree(t, main, "wt1")
	writeAt(t, main, "stele.md", "Rule.\n")
	writeAt(t, wt, "internal/x.go", "package x\n")

	got, err := findRoot(filepath.Join(wt, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	if got != wt {
		t.Errorf("findRoot in a worktree without stele.md = %s, want the worktree %s", got, wt)
	}
}
