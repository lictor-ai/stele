package compile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// listing is a generated file holding each rule's body, one per line.
var listing = Generated{Path: "gen/rules.txt", Render: func(idx index.Index) []byte {
	var b strings.Builder
	for _, e := range idx.Rules {
		b.WriteString(strings.TrimSpace(e.Body) + "\n")
	}
	return []byte(b.String())
}}

func readGen(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "gen", "rules.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCompileWritesGenerated(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}

	r, err := Compile(context.Background(), root, []source.Source{stelemd.New()}, gens, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || !r.Files[0].Changed || r.Files[0].Path != listing.Path {
		t.Fatalf("Files = %+v, want the listing, changed", r.Files)
	}
	want := "Never build SQL by string concatenation.\nEvery handler goes through the middleware chain.\n"
	if got := readGen(t, root); got != want {
		t.Errorf("generated file = %q, want %q", got, want)
	}

	// Unchanged: not rewritten.
	path := filepath.Join(root, "gen", "rules.txt")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	r, err = Compile(context.Background(), root, []source.Source{stelemd.New()}, gens, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if r.Files[0].Changed {
		t.Error("an unchanged file planned as changed")
	}
	if info, _ := os.Stat(path); !info.ModTime().Equal(old) {
		t.Error("an unchanged file was rewritten")
	}

	// A checkout with CRLF and a byte order mark is the same file.
	crlf := "\xef\xbb\xbf" + strings.ReplaceAll(want, "\n", "\r\n")
	if err := os.WriteFile(path, []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Plan(context.Background(), root, []source.Source{stelemd.New()}, gens, ids())
	if err != nil {
		t.Fatal(err)
	}
	if r.Files[0].Changed {
		t.Error("a CRLF copy of the same file planned as changed")
	}
}

func TestPlanReportsGeneratedAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	r, err := Plan(context.Background(), root, []source.Source{stelemd.New()}, []Generated{listing}, ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || !r.Files[0].Changed || !strings.Contains(string(r.Files[0].Data), "Never build SQL") {
		t.Errorf("Files = %+v", r.Files)
	}
	if _, err := os.Stat(filepath.Join(root, "gen")); !os.IsNotExist(err) {
		t.Error("Plan wrote the generated file")
	}
}

// The index can be fresh while the file is stale or gone: a branch switch, a
// deleted file. EnsureFresh rewrites only the file then.
func TestEnsureFreshRewritesAStaleGenerated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	if _, err := Compile(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	want := readGen(t, root)

	for name, mutate := range map[string]func(string) error{
		"stale":   func(p string) error { return os.WriteFile(p, []byte("old rules\n"), 0o644) },
		"missing": os.Remove,
	} {
		if err := mutate(filepath.Join(root, "gen", "rules.txt")); err != nil {
			t.Fatal(err)
		}
		_, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rebuilt {
			t.Errorf("%s: the index was rebuilt; only the file was stale", name)
		}
		if got := readGen(t, root); got != want {
			t.Errorf("%s: file = %q, want %q", name, got, want)
		}
	}
}

func TestEnsureFreshWritesGeneratedOnRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	if _, err := Compile(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	writeRules(t, root, "A brand new rule.\n")
	if _, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil || !rebuilt {
		t.Fatalf("EnsureFresh: rebuilt %v, %v", rebuilt, err)
	}
	if got := readGen(t, root); got != "A brand new rule.\n" {
		t.Errorf("file = %q", got)
	}
}

// A directory on the way that is a link is refused, after the lock and the
// index are saved: the rules still apply.
func TestGeneratedRefusesALinkedDirectory(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "gen")); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	_, err := Compile(context.Background(), root, []source.Source{stelemd.New()}, []Generated{listing}, ids(), testKey)
	var ge *GeneratedError
	if !errors.As(err, &ge) || ge.Path != listing.Path {
		t.Fatalf("Compile = %v, want a GeneratedError for %s", err, listing.Path)
	}
	if _, err := index.Load(root, testKey); err != nil {
		t.Errorf("the index was not saved: %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Error("stele wrote through the link")
	}
}
