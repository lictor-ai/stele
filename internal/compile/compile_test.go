package compile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// ids returns a newID that mints id-1, id-2, ...
func ids() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("id-%d", n)
	}
}

// counting wraps a source and counts its calls.
type counting struct {
	source.Source
	reads, fingerprints int
}

func (c *counting) Read(ctx context.Context, root string) ([]source.Rule, string, error) {
	c.reads++
	return c.Source.Read(ctx, root)
}

func (c *counting) Fingerprint(ctx context.Context, root string) (string, error) {
	c.fingerprints++
	return c.Source.Fingerprint(ctx, root)
}

// fake is an external source with fixed rules. It fails the test if called
// when it must not be.
type fake struct {
	rules []source.Rule
	fp    string
	calls int
}

func (f *fake) Name() string { return "org" }

func (f *fake) Read(context.Context, string) ([]source.Rule, string, error) {
	f.calls++
	return f.rules, f.fp, nil
}

func (f *fake) Fingerprint(context.Context, string) (string, error) {
	f.calls++
	return f.fp, nil
}

func newFake() *fake {
	return &fake{
		rules: []source.Rule{{Source: "org", Position: 0, Body: "Log through the shared logger.", ID: "ORG-1"}},
		fp:    "org-fp",
	}
}

const twoRules = "tags: [db]\n\nNever build SQL by string concatenation.\n\n---\n\nEvery handler goes through the middleware chain.\n"

func writeRules(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// files lists every file under root, relative to it.
func files(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func entryIDs(idx index.Index) []string {
	var out []string
	for _, e := range idx.Rules {
		out = append(out, e.ID)
	}
	return out
}

func TestPlanWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	r, err := Plan(context.Background(), root, []source.Source{stelemd.New(), newFake()}, ids())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !r.LockChanged || !r.IndexChanged {
		t.Errorf("first plan: LockChanged=%v IndexChanged=%v, want both true", r.LockChanged, r.IndexChanged)
	}
	if diff := cmp.Diff([]string{"stele.md"}, files(t, root)); diff != "" {
		t.Errorf("Plan wrote files (-want +got):\n%s", diff)
	}
}

func TestPlanApply(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	srcs := []source.Source{stelemd.New(), newFake()}

	r, err := Plan(context.Background(), root, srcs, ids())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if diff := cmp.Diff([]string{"id-1", "id-2", "ORG-1"}, entryIDs(r.Index)); diff != "" {
		t.Errorf("ids (-want +got):\n%s", diff)
	}
	if r.Index.Project != "id-3" || r.Lock.Project != "id-3" {
		t.Errorf("project: index %q, lock %q, want id-3 in both", r.Index.Project, r.Lock.Project)
	}
	e := r.Index.Rules[0]
	if e.Source != "stelemd" || e.Position != 0 || e.BodyHash != source.Hash(e.Body) || len(e.Tags) != 1 {
		t.Errorf("first entry = %+v, want its source, position, tags and hash kept", e)
	}
	if err := Apply(root, r); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{".stele/.gitignore", ".stele/index.json", "stele.lock", "stele.md"}
	if diff := cmp.Diff(want, files(t, root)); diff != "" {
		t.Errorf("files after Apply (-want +got):\n%s", diff)
	}
	gotIdx, err := index.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !index.Equal(r.Index, gotIdx) {
		t.Error("index on disk differs from the planned one")
	}
	gotLock, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !lock.Equal(r.Lock, gotLock) {
		t.Error("lock on disk differs from the planned one")
	}

	// Nothing changed, so nothing is rebuilt.
	again, err := Plan(context.Background(), root, srcs, ids())
	if err != nil {
		t.Fatal(err)
	}
	if again.LockChanged || again.IndexChanged {
		t.Errorf("unchanged rules: LockChanged=%v IndexChanged=%v, want both false", again.LockChanged, again.IndexChanged)
	}
}

func TestRecompileKeepsIDs(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	srcs := []source.Source{stelemd.New()}
	r, err := Plan(context.Background(), root, srcs, ids())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, r); err != nil {
		t.Fatal(err)
	}

	// Reword the second rule and add a third.
	writeRules(t, root, twoRules+"\n---\n\nPrefer table-driven tests.\n")
	writeRules(t, root, "tags: [db]\n\nNever build SQL by string concatenation.\n\n---\n\nEvery HTTP handler uses the standard middleware.\n\n---\n\nPrefer table-driven tests.\n")
	next, err := Plan(context.Background(), root, srcs, func() string { return "new" })
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"id-1", "id-2", "new"}, entryIDs(next.Index)); diff != "" {
		t.Errorf("ids after edit (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"id-2"}, next.Report.Guessed); diff != "" {
		t.Errorf("guessed (-want +got):\n%s", diff)
	}
	if !next.LockChanged {
		t.Error("LockChanged = false after an edit, want true: the lock is out of date")
	}
	if next.Index.Project != r.Index.Project {
		t.Errorf("project changed from %q to %q", r.Index.Project, next.Index.Project)
	}
}

func TestPlanErrors(t *testing.T) {
	t.Run("parse error names the line", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, "tags: [a]\n")
		_, err := Plan(context.Background(), root, []source.Source{stelemd.New()}, ids())
		var pe *source.ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v, want a *source.ParseError", err)
		}
		if err.Error() != "stele.md:1: rule has no body" {
			t.Errorf("err = %q, want it unprefixed", err)
		}
	})
	t.Run("duplicate id in the lock", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, twoRules)
		dup := `{"version": 1, "project": "p", "rules": [
			{"id": "a", "source": "stelemd", "hash": "h1", "position": 0},
			{"id": "a", "source": "stelemd", "hash": "h2", "position": 1}]}`
		if err := os.WriteFile(filepath.Join(root, lock.FileName), []byte(dup), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Plan(context.Background(), root, []source.Source{stelemd.New()}, ids())
		if !errors.Is(err, lock.ErrDuplicateID) {
			t.Errorf("err = %v, want ErrDuplicateID", err)
		}
	})
}

func TestEnsureFresh(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	org := newFake()
	r, err := Plan(ctx, root, []source.Source{stelemd.New(), org}, ids())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, r); err != nil {
		t.Fatal(err)
	}
	org.calls = 0

	t.Run("unchanged", func(t *testing.T) {
		md := &counting{Source: stelemd.New()}
		idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{md}, ids())
		if err != nil {
			t.Fatal(err)
		}
		if rebuilt || md.reads != 0 {
			t.Errorf("rebuilt=%v reads=%d, want no rebuild and no read", rebuilt, md.reads)
		}
		if diff := cmp.Diff(entryIDs(r.Index), entryIDs(idx)); diff != "" {
			t.Errorf("ids (-want +got):\n%s", diff)
		}
	})

	t.Run("edited stele.md keeps external rules", func(t *testing.T) {
		writeRules(t, root, twoRules+"\n---\n\nPrefer table-driven tests.\n")
		idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, func() string { return "new" })
		if err != nil {
			t.Fatal(err)
		}
		if !rebuilt {
			t.Error("rebuilt = false after an edit")
		}
		if diff := cmp.Diff([]string{"id-1", "id-2", "new", "ORG-1"}, entryIDs(idx)); diff != "" {
			t.Errorf("ids (-want +got):\n%s", diff)
		}
		if idx.Sources["org"] != "org-fp" {
			t.Errorf("org fingerprint = %q, want it carried over", idx.Sources["org"])
		}
		onDisk, err := index.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if !index.Equal(idx, onDisk) {
			t.Error("index on disk differs from the returned one")
		}
		lk, err := lock.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(lk.Rules) != 4 || lk.Rules[3] != (lock.Entry{ID: "ORG-1", Source: "org", Hash: source.Hash(org.rules[0].Body), Position: 0}) {
			t.Errorf("lock rules = %+v, want the external rule kept at the end", lk.Rules)
		}
	})

	t.Run("no index builds from builtins alone", func(t *testing.T) {
		if err := os.Remove(index.Path(root)); err != nil {
			t.Fatal(err)
		}
		idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, ids())
		if err != nil {
			t.Fatal(err)
		}
		if !rebuilt {
			t.Error("rebuilt = false with no index")
		}
		for _, e := range idx.Rules {
			if e.Source != "stelemd" {
				t.Errorf("rule %s from %s, want builtins only", e.ID, e.Source)
			}
		}
		// The external rule is not in the index, but the lock keeps its id
		// for the next stele compile.
		lk, err := lock.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		var kept bool
		for _, e := range lk.Rules {
			kept = kept || e.ID == "ORG-1"
		}
		if !kept {
			t.Errorf("lock rules = %+v, want ORG-1 kept", lk.Rules)
		}
	})

	if org.calls != 0 {
		t.Errorf("the external source was called %d times, want never (D45)", org.calls)
	}
}
