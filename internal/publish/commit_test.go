package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newCache makes a bare cache cloned from a remote holding files, and
// returns the cache directory.
func newCache(t *testing.T, files map[string]string) string {
	t.Helper()
	g := testGit(t)
	setCacheDir(t)
	remote := newRemote(t, files)
	setLocalRemote(t, remote)
	dir, err := cachePath("https://example.invalid/me/rules.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syncCache(context.Background(), g, dir, remote, remoteState{Base: "main"}, "refs/heads/stele/publish/x.md"); err != nil {
		t.Fatal(err)
	}
	return dir
}

var testCommitted = committed{Name: "Ada", Email: "ada@example.com", Project: "p-1", Head: "abc123"}

func TestBuildCommit(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := newCache(t, map[string]string{"README.md": "hi\n", "other/x.go": "package x\n", "a/stele.md": "old\n"})
	blob, err := hashBlob(ctx, g, dir, []byte("new\n"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := buildCommit(ctx, g, dir, "refs/heads/main", "a/stele.md", blob, testCommitted)
	if err != nil {
		t.Fatal(err)
	}
	tree := gitT(t, dir, "--git-dir="+dir, "ls-tree", "-r", "--name-only", commit)
	if tree != "README.md\na/stele.md\nother/x.go" {
		t.Errorf("tree =\n%s", tree)
	}
	if got := gitT(t, dir, "--git-dir="+dir, "cat-file", "blob", commit+":a/stele.md"); got != "new" {
		t.Errorf("a/stele.md = %q", got)
	}
	if got := gitT(t, dir, "--git-dir="+dir, "rev-parse", commit+"^"); got != gitT(t, dir, "--git-dir="+dir, "rev-parse", "main") {
		t.Errorf("parent = %s, want main", got)
	}
	raw := gitT(t, dir, "--git-dir="+dir, "cat-file", "commit", commit)
	for _, want := range []string{"author Ada <ada@example.com>", "committer Ada <ada@example.com>"} {
		if !strings.Contains(raw, want) {
			t.Errorf("commit lacks %q:\n%s", want, raw)
		}
	}
	msg := gitT(t, dir, "--git-dir="+dir, "log", "-1", "--format=%B", commit)
	if want := "stele: publish a/stele.md\n\nStele-Project: p-1\nProject-Commit: abc123"; msg != want {
		t.Errorf("message =\n%s\nwant\n%s", msg, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "index")); err == nil {
		t.Error("the bare clone has an index")
	}
}

func TestBuildCommitNewPath(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := newCache(t, map[string]string{"README.md": "hi\n"})
	blob, _ := hashBlob(ctx, g, dir, []byte("rules\n"))
	commit, err := buildCommit(ctx, g, dir, "refs/heads/main", "p/q/stele.md", blob, testCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if tree := gitT(t, dir, "--git-dir="+dir, "ls-tree", "-r", "--name-only", commit); tree != "README.md\np/q/stele.md" {
		t.Errorf("tree =\n%s", tree)
	}
}

func TestIsOurs(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := newCache(t, map[string]string{"README.md": "hi\n"})
	blob, _ := hashBlob(ctx, g, dir, []byte("rules\n"))
	ours, err := buildCommit(ctx, g, dir, "refs/heads/main", "a/stele.md", blob, testCommitted)
	if err != nil {
		t.Fatal(err)
	}
	tree := gitT(t, dir, "--git-dir="+dir, "rev-parse", ours+"^{tree}")
	commitWith := func(msg string) string {
		return gitT(t, dir, "--git-dir="+dir, "commit-tree", tree, "-p", "main", "-m", msg)
	}
	tests := []struct {
		name   string
		commit string
		want   bool
	}{
		{"ours", ours, true},
		{"a person's", commitWith("fix a typo"), false},
		{"another project's", commitWith("stele: publish a/stele.md\n\nStele-Project: p-2"), false},
		{"another path", commitWith("stele: publish b/stele.md\n\nStele-Project: p-1"), false},
		{"trailer in the subject", commitWith("stele: publish a/stele.md\nStele-Project: p-1"), false},
	}
	for _, tt := range tests {
		got, err := isOurs(ctx, g, dir, tt.commit, "a/stele.md", "p-1")
		if err != nil || got != tt.want {
			t.Errorf("%s: isOurs = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
}
