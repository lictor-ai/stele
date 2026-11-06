package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// D81: on a fresh clone the index is rebuilt from the builtins alone. It
// lacks an external source's rules, so the committed generated file, which
// holds them, is left as it is.
func TestEnsureFreshKeepsRulesFileWithoutExternalSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), newFake()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	before := readGen(t, root)
	if !strings.Contains(before, "Log through the shared logger.") {
		t.Fatalf("compile wrote %q, without the external rule", before)
	}
	lockBefore := lockBytes(t, root)

	if err := os.Remove(index.Path(root)); err != nil {
		t.Fatal(err)
	}
	idx, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := readGen(t, root); got != before {
		t.Errorf("generated file after the hook's rebuild = %q, want it unchanged, %q", got, before)
	}
	if string(lockBytes(t, root)) != string(lockBefore) {
		t.Error("the hook's rebuild changed stele.lock")
	}
	if len(idx.Rules) != 2 {
		t.Errorf("index holds %d rules, want the 2 from stele.md", len(idx.Rules))
	}
	// A stele.md edit still rebuilds the index, and still leaves the file.
	writeRules(t, root, twoRules+"\n---\n\nPrefer table-driven tests.\n")
	if _, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	if got := readGen(t, root); got != before {
		t.Errorf("generated file after an edit = %q, want it unchanged", got)
	}
}

// stele compile reads every source, so it writes the external rules, and a
// source removed from the list drops out of the file.
func TestCompileWritesExternalRulesThenDropsARemovedSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	r, err := Compile(ctx, root, []source.Source{stelemd.New(), newFake()}, gens, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Files[0].Changed || !strings.Contains(readGen(t, root), "shared logger") {
		t.Fatalf("Files = %+v, file %q", r.Files, readGen(t, root))
	}
	r, err = Compile(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Files[0].Changed || strings.Contains(readGen(t, root), "shared logger") {
		t.Errorf("after the source went: Files = %+v, file %q", r.Files, readGen(t, root))
	}
}

// pullAddingAnExternalRule compiles root with org's one rule, then as a
// teammate would with a second org rule, and puts back this machine's index:
// stele.lock and the generated file are as pulled, the index is older.
func pullAddingAnExternalRule(t *testing.T, root string, gens []Generated) (lockPulled []byte, genPulled string) {
	t.Helper()
	ctx := context.Background()
	writeRules(t, root, twoRules)
	org := newFake()
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	ours, err := os.ReadFile(index.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	org.rules = append(org.rules, source.Rule{Source: "org", Position: 1, Body: "Wrap every error.", ID: "ORG-2"})
	org.fp = "org-fp-2"
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index.Path(root), ours, 0o644); err != nil {
		t.Fatal(err)
	}
	return lockBytes(t, root), readGen(t, root)
}

// D81: a pull added a rule to an external source the index carries. The
// hook can't read it, so it leaves the pulled file and lock as they are.
func TestEnsureFreshAfterAPullAddingAnExternalRule(t *testing.T) {
	gens := []Generated{listing}
	root := t.TempDir()
	lockPulled, genPulled := pullAddingAnExternalRule(t, root, gens)
	if !strings.Contains(genPulled, "Wrap every error.") {
		t.Fatalf("pulled file lacks the new rule: %q", genPulled)
	}
	if _, _, err := EnsureFresh(context.Background(), root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	if got := readGen(t, root); got != genPulled {
		t.Errorf("generated file = %q, want it as pulled %q", got, genPulled)
	}
	if got := lockBytes(t, root); string(got) != string(lockPulled) {
		t.Errorf("stele.lock rewritten:\n%s", got)
	}
}

// The same pull and a stele.md edit: the hook rebuilds, keeps the pulled
// rule's lock entry, and still leaves the file.
func TestEnsureFreshAfterAPullAndAnEditKeepsTheNewEntry(t *testing.T) {
	gens := []Generated{listing}
	root := t.TempDir()
	_, genPulled := pullAddingAnExternalRule(t, root, gens)
	writeRules(t, root, twoRules+"\n---\n\nPrefer table-driven tests.\n")
	if _, _, err := EnsureFresh(context.Background(), root, []source.Source{stelemd.New()}, gens, func() string { return "new-rule" }, testKey); err != nil {
		t.Fatal(err)
	}
	f, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range f.Rules {
		found = found || e.ID == "ORG-2"
	}
	if !found {
		t.Errorf("the pulled rule's entry was dropped: %+v", f.Rules)
	}
	if got := readGen(t, root); got != genPulled {
		t.Errorf("generated file = %q, want it as pulled", got)
	}
}

// A stele.lock stele cannot read, here with a merge's conflict markers, means
// the hook can't tell what the generated files must hold: it leaves them.
func TestEnsureFreshLeavesFilesWithAnUnreadableLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), newFake()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "gen", "rules.txt")
	if err := os.WriteFile(stale, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, lock.FileName)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append([]byte("<<<<<<< HEAD\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _ = EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey)
	if got := readGen(t, root); got != "stale\n" {
		t.Errorf("generated file rewritten to %q with an unreadable lock", got)
	}
}

// Rules that would make a generated file larger than stele reads back fail
// the compile, rather than write a file the next run calls foreign.
func TestCompileRefusesAnOversizedGeneratedFile(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	org := newFake()
	org.rules[0].Body = strings.Repeat("x", maxGenerated+1)
	_, err := Compile(context.Background(), root, []source.Source{stelemd.New(), org}, []Generated{listing}, ids(), testKey)
	if err == nil || !strings.Contains(err.Error(), "gen/rules.txt") {
		t.Fatalf("Compile = %v, want an error naming the file", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gen", "rules.txt")); err == nil {
		t.Error("the file was written")
	}
}
