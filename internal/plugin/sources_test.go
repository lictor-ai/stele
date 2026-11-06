package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// noGit checks repos as gitexec does, roughly, and must never be asked to
// fetch: building the sources fetches nothing.
type noGit struct{ t *testing.T }

func (g noGit) Remote(repo string) (string, error) {
	host, path, ok := strings.Cut(strings.TrimPrefix(repo, "git@"), ":")
	if !ok || !strings.HasPrefix(repo, "git@") {
		return "", fmt.Errorf("repo %q: not a remote URL", repo)
	}
	return host + "/" + strings.TrimSuffix(path, ".git"), nil
}
func (g noGit) Resolve(context.Context, string, string) (string, error) {
	g.t.Error("Resolve called")
	return "", nil
}
func (g noGit) Ensure(context.Context, string, string, string) error {
	g.t.Error("Ensure called")
	return nil
}
func (g noGit) File(context.Context, string, string, string, int) ([]byte, error) {
	g.t.Error("File called")
	return nil, nil
}

func steleJSON(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSourcesNoSteleJSON(t *testing.T) {
	srcs, err := Sources(t.TempDir(), noGit{t}, gitrepo.Pinned, nil)
	if err != nil || len(srcs) != 1 || srcs[0].Name() != "stelemd" {
		t.Errorf("Sources = %v, %v; want the builtins", srcs, err)
	}
}

func TestSourcesListsGitSources(t *testing.T) {
	root := steleJSON(t, `{"block_on_error": true, "sources": [
		{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}},
		{"name": "git", "config": {"repo": "git@github.com:acme/g.git", "ref": "v1", "path": "go/stele.md"}}
	]}`)
	srcs, err := Sources(root, noGit{t}, gitrepo.Pinned, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range srcs {
		names = append(names, s.Name())
	}
	want := "stelemd git:github.com/acme/g/stele.md git:github.com/acme/g/go/stele.md"
	if strings.Join(names, " ") != want {
		t.Errorf("sources = %v, want %s", names, want)
	}
}

func TestSourcesRefusals(t *testing.T) {
	for name, content := range map[string]string{
		"built in":    `{"sources": [{"name": "stelemd"}]}`,
		"regex":       `{"sources": [{"name": "regex"}]}`,
		"bad name":    `{"sources": [{"name": "../x"}]}`,
		"dots":        `{"sources": [{"name": "a.b"}]}`,
		"ext twice":   `{"sources": [{"name": "docs"}, {"name": "docs"}]}`,
		"git timeout": `{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/g.git"}, "timeout": "2m"}]}`,
		"bad timeout": `{"sources": [{"name": "docs", "timeout": "soon"}]}`,
		"short":       `{"sources": [{"name": "docs", "timeout": "10ms"}]}`,
		"long":        `{"sources": [{"name": "docs", "timeout": "31m"}]}`,
		"ext key":     `{"sources": [{"name": "docs", "timeot": "2m"}]}`,
		"unknown key": `{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/g.git", "branch": "main"}}]}`,
		"entry key":   `{"sources": [{"name": "git", "confg": {"repo": "git@github.com:a/g.git"}}]}`,
		"no repo":     `{"sources": [{"name": "git", "config": {}}]}`,
		"no config":   `{"sources": [{"name": "git"}]}`,
		"bad repo":    `{"sources": [{"name": "git", "config": {"repo": "/tmp/g"}}]}`,
		"bad ref":     `{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/g.git", "ref": "--upload-pack=x"}}]}`,
		"twice":       `{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/g.git"}}, {"name": "git", "config": {"repo": "git@github.com:a/g.git", "ref": "dev"}}]}`,
		"not a list":  `{"sources": {"name": "git"}}`,
		"not json":    `{`,
		"too many":    `{"sources": [` + strings.TrimSuffix(strings.Repeat(`{"name": "git", "config": {"repo": "git@h:a/g.git", "path": "x.md"}},`, 17), ",") + `]}`,
	} {
		_, err := Sources(steleJSON(t, content), noGit{t}, gitrepo.Pinned, &fakeRunner{})
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "stele.json") {
			t.Errorf("%s: %v, want it to name stele.json", name, err)
		}
	}
}

func TestSourcesNamesTheEntry(t *testing.T) {
	_, err := Sources(steleJSON(t, `{"sources": [{"name": "git", "config": {"repo": "git@github.com:a/g.git"}}, {"name": "No.pe"}]}`), noGit{t}, gitrepo.Pinned, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "sources[1]") {
		t.Errorf("err = %v, want it to name sources[1]", err)
	}
	if !errors.Is(err, ErrSources) {
		t.Errorf("err = %v, want ErrSources", err)
	}
}
