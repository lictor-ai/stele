package compile

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
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
