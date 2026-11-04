package compile

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

const (
	pinA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// pinnedFake is a pinned source with fixed rules. It records the pins it was
// given, and pins commit.
type pinnedFake struct {
	commit string
	body   string
	got    []source.Pin
	calls  int
}

func (p *pinnedFake) Name() string { return "git:example.com/g/stele.md" }

func (p *pinnedFake) Read(ctx context.Context, root string) ([]source.Rule, string, error) {
	rules, fp, _, err := p.ReadPinned(ctx, root, nil)
	return rules, fp, err
}

func (p *pinnedFake) ReadPinned(_ context.Context, _ string, pins []source.Pin) ([]source.Rule, string, source.Pin, error) {
	p.calls++
	p.got = pins
	rules := []source.Rule{{Source: p.Name(), Position: 0, Body: p.body}}
	pin := source.Pin{Source: p.Name(), Repo: "git@example.com:g.git", Path: "stele.md", Commit: p.commit}
	return rules, p.commit, pin, nil
}

func (p *pinnedFake) Fingerprint(context.Context, string) (string, error) {
	p.calls++
	return p.commit, nil
}

func shared(commit string) *pinnedFake {
	return &pinnedFake{commit: commit, body: "Shared: wrap every error with context."}
}

func TestCompileRecordsPins(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	g := shared(pinA)
	r, err := Compile(ctx, root, []source.Source{stelemd.New(), g}, []Generated{listing}, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.got) != 0 {
		t.Errorf("first compile gave the source pins %v, want none", g.got)
	}
	if len(r.Lock.Sources) != 1 || r.Lock.Sources[0].Commit != pinA {
		t.Fatalf("lock pins = %+v, want %s", r.Lock.Sources, pinA)
	}
	f, err := lock.Load(root)
	if err != nil || len(f.Sources) != 1 || f.Version != 2 {
		t.Fatalf("saved lock = %+v, %v; want version 2 with one pin", f, err)
	}
	if !strings.Contains(readGen(t, root), "Shared: wrap every error") {
		t.Errorf("generated file lacks the shared rule:\n%s", readGen(t, root))
	}

	// The next compile hands the source the pins it recorded.
	g2 := shared(pinA)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), g2}, []Generated{listing}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	if len(g2.got) != 1 || g2.got[0].Commit != pinA {
		t.Errorf("second compile gave the source %v, want the pin of %s", g2.got, pinA)
	}
}

func TestPlanSeesAMovedPin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), shared(pinA)}, nil, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	r, err := Plan(ctx, root, []source.Source{stelemd.New(), shared(pinB)}, nil, ids())
	if err != nil {
		t.Fatal(err)
	}
	if !r.LockChanged {
		t.Error("a moved pin is not LockChanged")
	}
}

// The hook rebuilds from the builtins alone and must keep every pin and every
// shared rule's entry, so stele.lock is never rewritten without them.
func TestEnsureFreshKeepsPins(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), shared(pinA)}, []Generated{listing}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	before := lockBytes(t, root)
	writeRules(t, root, twoRules+"\n---\n\nPrefer table-driven tests.\n")
	if _, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, []Generated{listing}, func() string { return "new" }, testKey); err != nil || !rebuilt {
		t.Fatalf("EnsureFresh = %v, %v; want a rebuild", rebuilt, err)
	}
	f, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sources) != 1 || f.Sources[0].Commit != pinA {
		t.Errorf("pins after the rebuild = %+v, want %s kept\nbefore:\n%s", f.Sources, pinA, before)
	}
	if !strings.Contains(readGen(t, root), "Shared: wrap every error") {
		t.Errorf("the rebuild dropped the shared rule from the generated file:\n%s", readGen(t, root))
	}
}

// With no index, the hook knows only the builtins' rules: it must not
// rewrite the committed rules file without the shared ones, nor the lock.
func TestEnsureFreshWithNoIndexLeavesPinnedFilesAlone(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), shared(pinA)}, []Generated{listing}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	gen, lck := readGen(t, root), lockBytes(t, root)
	if err := os.Remove(index.Path(root)); err != nil {
		t.Fatal(err)
	}
	idx, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, []Generated{listing}, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Rules) != 2 {
		t.Errorf("index holds %d rules, want the 2 local ones", len(idx.Rules))
	}
	if got := readGen(t, root); got != gen {
		t.Errorf("generated file rewritten:\n%s\nwant\n%s", got, gen)
	}
	if got := lockBytes(t, root); string(got) != string(lck) {
		t.Errorf("stele.lock rewritten:\n%s\nwant\n%s", got, lck)
	}
}

// stele stats plans with the builtins and the index, never a pinned source.
func TestPlanLocalCarriesOtherSources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), shared(pinA)}, []Generated{listing}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	before := files(t, root)
	r, err := PlanLocal(ctx, root, []source.Source{stelemd.New()}, []Generated{listing}, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Index.Rules) != 3 {
		t.Errorf("PlanLocal holds %d rules, want 3, the shared one carried", len(r.Index.Rules))
	}
	if r.LockChanged {
		t.Error("PlanLocal reports stele.lock out of date")
	}
	if after := files(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("PlanLocal wrote files: %v, before %v", after, before)
	}
}
