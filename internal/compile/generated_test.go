package compile

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
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

// idListing is a generated file holding each rule's id, as the rules file does.
var idListing = Generated{Path: "gen/rules.txt", Render: func(idx index.Index) []byte {
	return []byte(strings.Join(entryIDs(idx), "\n") + "\n")
}}

// A pull brings a stele.lock with new ids and the rules file generated from
// it, and leaves stele.md alone. The index follows the lock: it must not put
// its old ids back in the committed file. Anything that does not change what
// a rule's id is leaves the index alone.
func TestEnsureFreshFollowsAPulledLock(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	rewrite := func(t *testing.T, root string, change func(*lock.File)) {
		t.Helper()
		f, err := lock.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		change(&f)
		if err := lock.Save(root, f); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		pull func(t *testing.T, root string)
		// rebuilt says the index is rebuilt.
		rebuilt bool
		// lockRewritten says stele.md wins over the pulled lock.
		lockRewritten bool
		wantIDs       []string
		wantProject   string
	}{{
		name: "new ids",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) { f.Rules[0].ID, f.Rules[1].ID = "pulled-1", "pulled-2" })
		},
		rebuilt: true,
		wantIDs: []string{"pulled-1", "pulled-2"},
	}, {
		name: "new project",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) { f.Project = "pulled-project" })
		},
		rebuilt:     true,
		wantIDs:     []string{"id-1", "id-2"},
		wantProject: "pulled-project",
	}, {
		name: "a reworded entry",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) { f.Rules[0].Hash = "not-this-rule" })
		},
		rebuilt:       true,
		lockRewritten: true,
		wantIDs:       []string{"id-1", "id-2"},
	}, {
		name: "reordered",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) { f.Rules[0], f.Rules[1] = f.Rules[1], f.Rules[0] })
		},
		wantIDs: []string{"id-1", "id-2"},
	}, {
		name: "an extra entry",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) {
				f.Rules = append(f.Rules, lock.Entry{ID: "extra", Source: "org", Hash: "h"})
			})
		},
		wantIDs: []string{"id-1", "id-2"},
	}, {
		name: "deleted",
		pull: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, lock.FileName)); err != nil {
				t.Fatal(err)
			}
		},
		wantIDs: []string{"id-1", "id-2"},
	}, {
		name: "conflict markers",
		pull: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, lock.FileName), []byte("<<<<<<< HEAD\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		wantIDs: []string{"id-1", "id-2"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeRules(t, root, twoRules)
			srcs := []source.Source{stelemd.New()}
			if _, err := Compile(ctx, root, srcs, gens, ids(), testKey); err != nil {
				t.Fatal(err)
			}
			first, err := lock.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			tt.pull(t, root)

			// The pulled file is what the pulled lock gives.
			gen := filepath.Join(root, "gen", "rules.txt")
			want := strings.Join(tt.wantIDs, "\n") + "\n"
			if err := os.WriteFile(gen, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			pulled, err := os.ReadFile(filepath.Join(root, lock.FileName))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			noMint := func() string {
				t.Error("the hook minted an id")
				return "minted"
			}

			idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint, testKey)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt != tt.rebuilt {
				t.Errorf("rebuilt = %v, want %v", rebuilt, tt.rebuilt)
			}
			if diff := cmp.Diff(tt.wantIDs, entryIDs(idx)); diff != "" {
				t.Errorf("index ids (-want +got):\n%s", diff)
			}
			project := tt.wantProject
			if project == "" {
				project = first.Project
			}
			if idx.Project != project {
				t.Errorf("index project = %q, want %q", idx.Project, project)
			}
			if got := readGen(t, root); got != want {
				t.Errorf("generated file = %q, want it left as %q", got, want)
			}
			after, err := os.ReadFile(filepath.Join(root, lock.FileName))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if same := bytes.Equal(after, pulled); same == tt.lockRewritten {
				t.Errorf("stele.lock rewritten = %v, want %v:\n%s", !same, tt.lockRewritten, after)
			}

			// It settles: the next write finds nothing to do.
			if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint, testKey); err != nil || rebuilt {
				t.Errorf("second EnsureFresh: rebuilt %v, %v", rebuilt, err)
			}
		})
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
