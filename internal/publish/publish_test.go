package publish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
)

// fixture is a project and the bare repository it publishes to.
type fixture struct {
	root   string // the project
	remote string // the bare repository on disk standing in for repo
}

// newFixture makes a project with rules whose stele.json publishes to repo
// at path, and a remote holding files, reached through localRemote.
func newFixture(t *testing.T, repo, path, rules string, files map[string]string) fixture {
	t.Helper()
	setCacheDir(t)
	f := fixture{root: newProject(t, ".", rules), remote: newRemote(t, files)}
	setLocalRemote(t, f.remote)
	cfg := `{"publish": {"repo": "` + repo + `"`
	if path != "" {
		cfg += `, "path": "` + path + `"`
	}
	writeFile(t, filepath.Join(f.root, "stele.json"), cfg+"}}")
	return f
}

// commitRules commits new rules in the project.
func (f fixture) commitRules(t *testing.T, rules string) {
	t.Helper()
	writeFile(t, filepath.Join(f.root, "stele.md"), rules)
	gitT(t, f.root, "commit", "--quiet", "-am", "edit rules")
}

// tip returns the remote's commit for ref, or "" if it has none.
func (f fixture) tip(t *testing.T, ref string) string {
	t.Helper()
	out, err := (gitCmd{bin: testGit(t).bin}).run(context.Background(), f.remote, nil, nil, "--git-dir="+f.remote, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		return ""
	}
	return trim(out)
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

const otherRepo = "https://example.invalid/me/rules.git"
const branch = "refs/heads/stele/publish/stele.md"

func TestRunPublishes(t *testing.T) {
	f := newFixture(t, otherRepo, "", "Use tabs.\n", map[string]string{"README.md": "hi\n", "x/y.go": "package y\n"})
	ctx := context.Background()

	res, err := Run(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || res.Unchanged || res.Base != "main" || res.Branch != "stele/publish/stele.md" {
		t.Errorf("result = %+v", res)
	}
	if res.Page != "" || res.MR != "" {
		t.Errorf("another host: page %q, mr %q; want none", res.Page, res.MR)
	}
	if got := gitT(t, f.remote, "cat-file", "blob", branch+":stele.md"); got != "Use tabs." {
		t.Errorf("published stele.md = %q", got)
	}
	if tree := gitT(t, f.remote, "ls-tree", "-r", "--name-only", branch); tree != "README.md\nstele.md\nx/y.go" {
		t.Errorf("tree =\n%s", tree)
	}
	first := f.tip(t, branch)

	// Again, unchanged: nothing is pushed.
	res, err = Run(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed || res.Unchanged {
		t.Errorf("second run = %+v, want not pushed, not unchanged", res)
	}
	if f.tip(t, branch) != first {
		t.Error("the branch moved")
	}

	// An edit replaces the branch, on top of the default branch.
	f.commitRules(t, "Use spaces.\n")
	res, err = Run(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed {
		t.Errorf("after an edit: %+v", res)
	}
	if parent := gitT(t, f.remote, "rev-parse", branch+"^"); parent != f.tip(t, "refs/heads/main") {
		t.Errorf("parent = %s, want main", parent)
	}

	// Once merged, there is nothing to publish.
	gitT(t, f.remote, "update-ref", "refs/heads/main", branch)
	res, err = Run(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unchanged {
		t.Errorf("after the merge: %+v, want unchanged", res)
	}
}

func TestRunNestedPath(t *testing.T) {
	f := newFixture(t, otherRepo, "project-a/stele.md", "Rules.\n", map[string]string{"README.md": "hi\n"})
	res, err := Run(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "stele/publish/project-a/stele.md" {
		t.Errorf("branch = %q", res.Branch)
	}
	if got := gitT(t, f.remote, "cat-file", "blob", "refs/heads/stele/publish/project-a/stele.md:project-a/stele.md"); got != "Rules." {
		t.Errorf("published = %q", got)
	}
}

func TestRunRefusesSomeoneElsesBranch(t *testing.T) {
	f := newFixture(t, otherRepo, "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	ctx := context.Background()
	if _, err := Run(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	tree := gitT(t, f.remote, "rev-parse", branch+"^{tree}")
	theirs := gitT(t, f.remote, "commit-tree", tree, "-p", branch, "-m", "fix a typo")
	gitT(t, f.remote, "update-ref", branch, theirs)

	f.commitRules(t, "Use spaces.\n")
	_, err := Run(ctx, f.root)
	if !errors.Is(err, ErrForeignBranch) {
		t.Fatalf("err = %v, want ErrForeignBranch", err)
	}
	if f.tip(t, branch) != theirs {
		t.Error("the branch was overwritten")
	}
}

func TestRunRefusesAnotherProjectsBranch(t *testing.T) {
	f := newFixture(t, otherRepo, "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	ctx := context.Background()
	if _, err := Run(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	if err := lock.Save(f.root, lock.File{Version: 1, Project: "p-2"}); err != nil {
		t.Fatal(err)
	}
	f.commitRules(t, "Other rules.\n")
	if _, err := Run(ctx, f.root); !errors.Is(err, ErrForeignBranch) {
		t.Errorf("err = %v, want ErrForeignBranch", err)
	}
}

func TestRunLeaseRefusesARace(t *testing.T) {
	f := newFixture(t, otherRepo, "", "Use tabs.\n", map[string]string{"README.md": "hi\n"})
	ctx := context.Background()
	if _, err := Run(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	f.commitRules(t, "Use spaces.\n")
	beforePush = func() { gitT(t, f.remote, "update-ref", branch, "refs/heads/main") }
	t.Cleanup(func() { beforePush = nil })
	if _, err := Run(ctx, f.root); !errors.Is(err, ErrGit) {
		t.Errorf("err = %v, want the push refused", err)
	}
	if f.tip(t, branch) != f.tip(t, "refs/heads/main") {
		t.Error("the push went through")
	}
}

func TestRunEmptyRepository(t *testing.T) {
	f := newFixture(t, otherRepo, "", "Use tabs.\n", nil)
	if _, err := Run(context.Background(), f.root); !errors.Is(err, ErrEmptyRepo) {
		t.Fatalf("err = %v, want ErrEmptyRepo", err)
	}
	dir, _ := cachePath(otherRepo)
	if _, err := os.Stat(dir); err == nil {
		t.Error("an empty repository left a cache")
	}
}

func TestRunNoConfig(t *testing.T) {
	root := newProject(t, ".", "x\n")
	if _, err := Run(context.Background(), root); !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v, want ErrNoConfig", err)
	}
}
