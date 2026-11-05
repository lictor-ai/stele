package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
)

const (
	repo    = "git@github.com:acme/guidelines.git"
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	idOne   = "2de7574e-7106-4986-8ce6-996d06e2703a"
)

// fakeGit serves files per commit, and the tip of each ref.
type fakeGit struct {
	tips     map[string]string            // ref → commit
	files    map[string]map[string]string // commit → path → content
	resolved []string                     // the refs Resolve was asked for
	ensured  []string                     // the commits Ensure was asked for
}

func (g *fakeGit) Remote(r string) (string, error) {
	if r != repo {
		return "", fmt.Errorf("repo %q: not a remote URL", r)
	}
	return "github.com/acme/guidelines", nil
}

func (g *fakeGit) Resolve(_ context.Context, _, ref string) (string, error) {
	g.resolved = append(g.resolved, ref)
	c, ok := g.tips[ref]
	if !ok {
		return "", fmt.Errorf("ref %q is not a branch or tag of the repository", ref)
	}
	return c, nil
}

func (g *fakeGit) Ensure(_ context.Context, _, _, commit string) error {
	g.ensured = append(g.ensured, commit)
	if _, ok := g.files[commit]; !ok {
		return errors.New("no longer on the remote")
	}
	return nil
}

func (g *fakeGit) File(_ context.Context, _, commit, path string, max int) ([]byte, error) {
	data, ok := g.files[commit][path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if len(data) > max {
		return nil, fmt.Errorf("over %d bytes", max)
	}
	return []byte(data), nil
}

func newGit() *fakeGit {
	return &fakeGit{
		tips: map[string]string{"": commitA, "main": commitA},
		files: map[string]map[string]string{
			commitA: {"stele.md": "Wrap every error.\n\n---\n\nscope: [\"**/*.go\"]\n\nNo panics in libraries.\n"},
			commitB: {"stele.md": "Wrap every error, with context.\n"},
		},
	}
}

func cfg() Config { return Config{Repo: repo, Ref: "main"} }

func mustNew(t *testing.T, c Config, g Git, m Mode) *Source {
	t.Helper()
	s, err := New(c, g, m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestName(t *testing.T) {
	s := mustNew(t, cfg(), newGit(), Pinned)
	if s.Name() != "git:github.com/acme/guidelines/stele.md" {
		t.Errorf("Name = %q", s.Name())
	}
	c := cfg()
	c.Path = "go/stele.md"
	if got := mustNew(t, c, newGit(), Pinned).Name(); got != "git:github.com/acme/guidelines/go/stele.md" {
		t.Errorf("Name with a path = %q", got)
	}
}

func TestReadResolvesWithNoPin(t *testing.T) {
	g := newGit()
	rules, fp, pin, err := mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1].Body != "No panics in libraries." || rules[1].Scope[0] != "**/*.go" {
		t.Fatalf("rules = %+v", rules)
	}
	for _, r := range rules {
		if r.Source != "git:github.com/acme/guidelines/stele.md" || r.File != "github.com/acme/guidelines/stele.md@aaaaaaa" {
			t.Errorf("rule %d: Source %q, File %q", r.Position, r.Source, r.File)
		}
	}
	want := source.Pin{Source: "git:github.com/acme/guidelines/stele.md", Repo: repo, Ref: "main", Path: "stele.md", Commit: commitA}
	if pin != want {
		t.Errorf("pin = %+v, want %+v", pin, want)
	}
	if !strings.HasPrefix(fp, commitA) {
		t.Errorf("fingerprint %q does not start with the commit", fp)
	}
}

func pinOf(c Config, commit string) []source.Pin {
	p := c.Path
	if p == "" {
		p = DefaultPath
	}
	return []source.Pin{{Source: "git:github.com/acme/guidelines/" + p, Repo: c.Repo, Ref: c.Ref, Path: p, Commit: commit}}
}

// The pin holds until stele.json changes or --update moves it.
func TestPinnedKeepsAMatchingPin(t *testing.T) {
	g := newGit()
	g.tips["main"] = commitB
	_, _, pin, err := mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), pinOf(cfg(), commitA))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Commit != commitA || len(g.resolved) != 0 {
		t.Errorf("pin = %s, resolved %v; want %s kept and no resolve", pin.Commit, g.resolved, commitA)
	}
	if len(g.ensured) != 1 || g.ensured[0] != commitA {
		t.Errorf("ensured %v, want the pinned commit", g.ensured)
	}
}

func TestPinnedRepinsWhenStelejsonChanges(t *testing.T) {
	g := newGit()
	g.tips["release"] = commitB
	c := cfg()
	c.Ref = "release"
	_, _, pin, err := mustNew(t, c, g, Pinned).ReadPinned(context.Background(), t.TempDir(), pinOf(cfg(), commitA))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Commit != commitB || pin.Ref != "release" {
		t.Errorf("pin = %+v, want release at %s", pin, commitB)
	}
}

func TestUpdateMovesThePin(t *testing.T) {
	g := newGit()
	g.tips["main"] = commitB
	rules, _, pin, err := mustNew(t, cfg(), g, Update).ReadPinned(context.Background(), t.TempDir(), pinOf(cfg(), commitA))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Commit != commitB || rules[0].Body != "Wrap every error, with context." {
		t.Errorf("pin = %s, rules %+v; want %s's", pin.Commit, rules, commitB)
	}
}

// stele check never moves a pin, and never resolves.
func TestFrozen(t *testing.T) {
	g := newGit()
	g.tips["main"] = commitB
	s := mustNew(t, cfg(), g, Frozen)
	_, _, pin, err := s.ReadPinned(context.Background(), t.TempDir(), pinOf(cfg(), commitA))
	if err != nil || pin.Commit != commitA {
		t.Errorf("Frozen with a pin = %s, %v; want %s", pin.Commit, err, commitA)
	}
	if _, _, _, err := s.ReadPinned(context.Background(), t.TempDir(), nil); !errors.Is(err, source.ErrUnpinned) {
		t.Errorf("Frozen with no pin = %v, want ErrUnpinned", err)
	}
	c := cfg()
	c.Path = "go/stele.md"
	other := mustNew(t, c, g, Frozen)
	if _, _, _, err := other.ReadPinned(context.Background(), t.TempDir(), pinOf(cfg(), commitA)); !errors.Is(err, source.ErrUnpinned) {
		t.Errorf("Frozen with a pin of another path = %v, want ErrUnpinned", err)
	}
	if len(g.resolved) != 0 {
		t.Errorf("Frozen resolved %v", g.resolved)
	}
}

func TestMissingFile(t *testing.T) {
	c := cfg()
	c.Path = "nope/stele.md"
	_, _, _, err := mustNew(t, c, newGit(), Pinned).ReadPinned(context.Background(), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "nope/stele.md") {
		t.Errorf("err = %v, want it to name the path", err)
	}
}

func TestParseErrorNamesTheSharedFile(t *testing.T) {
	g := newGit()
	g.files[commitA]["stele.md"] = "Fine.\n\n---\n\n```\nunclosed\n"
	_, _, _, err := mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), nil)
	var pe *source.ParseError
	if !errors.As(err, &pe) || pe.File != "github.com/acme/guidelines/stele.md@aaaaaaa" {
		t.Errorf("err = %v, want a ParseError naming the shared file", err)
	}
}

// The guidelines repository's own stele.lock supplies the ids (D52).
func TestIDsFromTheGuidelinesLock(t *testing.T) {
	g := newGit()
	rules, _, _, err := mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	gl := lock.File{Version: 1, Project: "guidelines", Rules: []lock.Entry{
		{ID: idOne, Source: "stelemd", Hash: source.Hash(rules[0].Body), Position: 0},
		{ID: "9c1e6a2b-0000-4000-8000-000000000001", Source: "stelemd", Hash: "elsewhere", Position: 1},
	}}
	g.files[commitA]["stele.lock"] = encode(t, gl)
	rules, _, _, err = mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].ID != idOne {
		t.Errorf("rule 0 id = %q, want %s from the guidelines lock", rules[0].ID, idOne)
	}
	if rules[1].ID != "" {
		t.Errorf("rule 1 id = %q: its hash is not in the guidelines lock, so none is supplied", rules[1].ID)
	}
}

func TestGuidelinesLockRefusals(t *testing.T) {
	for name, content := range map[string]string{
		"not json":    "{",
		"not a uuid":  `{"version": 1, "project": "p", "rules": [{"id": "SEC-001\n", "source": "stelemd", "hash": "%s", "position": 0}]}`,
		"upper uuid":  `{"version": 1, "project": "p", "rules": [{"id": "2DE7574E-7106-4986-8CE6-996D06E2703A", "source": "stelemd", "hash": "%s", "position": 0}]}`,
		"no projects": `{"version": 1, "rules": []}`,
	} {
		g := newGit()
		if strings.Contains(content, "%s") {
			content = fmt.Sprintf(content, source.Hash("Wrap every error."))
		}
		g.files[commitA]["stele.lock"] = content
		if _, _, _, err := mustNew(t, cfg(), g, Pinned).ReadPinned(context.Background(), t.TempDir(), nil); err == nil {
			t.Errorf("%s: the guidelines lock was accepted", name)
		}
	}
}

func TestNewRefusesBadSettings(t *testing.T) {
	for _, c := range []Config{
		{Repo: "ext::sh -c x"},
		{Repo: repo, Ref: "-x"},
		{Repo: repo, Ref: "a..b"},
		{Repo: repo, Ref: "a b"},
		{Repo: repo, Ref: "x.lock"},
		{Repo: repo, Ref: "x/"},
		{Repo: repo, Ref: "@{-1}"},
		{Repo: repo, Ref: "a~1"},
		{Repo: repo, Path: "../stele.md"},
		{Repo: repo, Path: "/etc/stele.md"},
		{Repo: repo, Path: "rules.txt"},
		{Repo: repo, Path: "a\\b.md"},
		{Repo: repo, Path: "-x.md"},
	} {
		if _, err := New(c, newGit(), Pinned); err == nil {
			t.Errorf("New(%+v) accepted", c)
		}
	}
	for _, ref := range []string{"main", "release/1.2", "v1.0.0", "refs/heads/main", "refs/tags/v1"} {
		if _, err := New(Config{Repo: repo, Ref: ref}, newGit(), Pinned); err != nil {
			t.Errorf("New(ref %q) = %v", ref, err)
		}
	}
}

// Read and Fingerprint take the pin from stele.lock, starting nothing.
func TestReadAndFingerprintUseTheLock(t *testing.T) {
	root := t.TempDir()
	f := lock.File{Version: 1, Project: "p", Sources: pinOf(cfg(), commitA)}
	if err := lock.Save(root, f); err != nil {
		t.Fatal(err)
	}
	g := newGit()
	g.tips["main"] = commitB
	s := mustNew(t, cfg(), g, Pinned)
	fp, err := s.Fingerprint(context.Background(), root)
	if err != nil || !strings.HasPrefix(fp, commitA) {
		t.Errorf("Fingerprint = %q, %v; want the pinned commit", fp, err)
	}
	if len(g.resolved)+len(g.ensured) != 0 {
		t.Errorf("Fingerprint reached git: %v %v", g.resolved, g.ensured)
	}
	rules, _, err := s.Read(context.Background(), root)
	if err != nil || rules[0].Body != "Wrap every error." {
		t.Errorf("Read = %+v, %v; want the pinned commit's rules", rules, err)
	}
}

func encode(t *testing.T, f lock.File) string {
	t.Helper()
	dir := t.TempDir()
	if err := lock.Save(dir, f); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, lock.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
