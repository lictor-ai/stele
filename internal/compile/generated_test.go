package compile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// rewrite changes stele.lock as a pull would.
func rewrite(t *testing.T, root string, change func(*lock.File)) {
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

// fileBytes is a file as it is on disk; nil when it is missing.
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return data
}

// lockBytes is stele.lock as it is on disk; nil when it is missing.
func lockBytes(t *testing.T, root string) []byte {
	t.Helper()
	return fileBytes(t, filepath.Join(root, lock.FileName))
}

func writeGen(t *testing.T, root string, ids []string) {
	t.Helper()
	gen := filepath.Join(root, "gen", "rules.txt")
	if err := os.WriteFile(gen, []byte(strings.Join(ids, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A pull brings a stele.lock with new ids and the rules file generated from
// it, and leaves stele.md alone. The index follows the lock: it must not put
// its old ids back in the committed file. A lock that stele.md disagrees with
// is left for stele check, and nothing is written. Anything that does not
// change what a rule's id is leaves the index alone.
func TestEnsureFreshFollowsAPulledLock(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	tests := []struct {
		name string
		pull func(t *testing.T, root string)
		// rebuilt says the index is rebuilt.
		rebuilt     bool
		wantIDs     []string
		wantProject string
		// genIDs is the pulled rules file's ids, when not wantIDs.
		genIDs []string
		// planMints says the plan needs an id, which it throws away.
		planMints bool
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
		wantIDs: []string{"id-1", "id-2"},
	}, {
		name: "a rule missing",
		pull: func(t *testing.T, root string) {
			rewrite(t, root, func(f *lock.File) { f.Rules = f.Rules[:1] })
		},
		wantIDs:   []string{"id-1", "id-2"},
		genIDs:    []string{"id-1"},
		planMints: true,
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
			genIDs := tt.genIDs
			if genIDs == nil {
				genIDs = tt.wantIDs
			}
			writeGen(t, root, genIDs)
			want := readGen(t, root)
			pulled := lockBytes(t, root)
			newID := noMint(t)
			if tt.planMints {
				newID = func() string { return "thrown-away" }
			}

			idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, newID, testKey)
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

			// It settles: the next write finds nothing to do, and neither
			// write touched the lock or the rules file.
			if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, newID, testKey); err != nil || rebuilt {
				t.Errorf("second EnsureFresh: rebuilt %v, %v", rebuilt, err)
			}
			if got := readGen(t, root); got != want {
				t.Errorf("generated file = %q, want it left as %q", got, want)
			}
			if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
				t.Errorf("stele.lock rewritten:\n%s\nwant it left as:\n%s", got, pulled)
			}
		})
	}
}

// noMint is a newID for a hook that must not need one.
func noMint(t *testing.T) func() string {
	return func() string {
		t.Error("the hook minted an id")
		return "minted"
	}
}

// git writes the files of a pull one at a time, so a hook can run with the
// new stele.lock and the old stele.md. It writes nothing then, or it would
// drop a teammate's rule from the lock and mint it another id once stele.md
// lands.
func TestEnsureFreshWaitsOutAPull(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	srcs := []source.Source{stelemd.New()}
	const newRules = "tags: [db]\n\nNever concatenate SQL.\n\n---\n\nEvery handler goes through the middleware chain.\n\n---\n\nPrefer table-driven tests.\n"

	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, srcs, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}

	// The teammate's repository: rule 1 reworded, teammate-1 added.
	theirs := t.TempDir()
	if err := os.WriteFile(filepath.Join(theirs, lock.FileName), lockBytes(t, root), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRules(t, theirs, newRules)
	mint := func() string { return "teammate-1" }
	if _, err := Compile(ctx, theirs, srcs, gens, mint, testKey); err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"id-1", "id-2", "teammate-1"}

	// The pull has reached stele.lock and the rules file, not stele.md.
	pulled := lockBytes(t, theirs)
	if err := os.WriteFile(filepath.Join(root, lock.FileName), pulled, 0o644); err != nil {
		t.Fatal(err)
	}
	writeGen(t, root, wantIDs)
	want := readGen(t, root)

	for hook := 1; hook <= 2; hook++ {
		idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, func() string { return "thrown-away" }, testKey)
		if err != nil || rebuilt {
			t.Fatalf("hook %d: rebuilt %v, %v", hook, rebuilt, err)
		}
		if diff := cmp.Diff([]string{"id-1", "id-2"}, entryIDs(idx)); diff != "" {
			t.Errorf("hook %d: index ids (-want +got):\n%s", hook, diff)
		}
		if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
			t.Errorf("hook %d: stele.lock rewritten:\n%s", hook, got)
		}
		if got := readGen(t, root); got != want {
			t.Errorf("hook %d: generated file = %q, want %q", hook, got, want)
		}
	}

	// stele.md lands.
	writeRules(t, root, newRules)
	idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey)
	if err != nil || !rebuilt {
		t.Fatalf("after stele.md: rebuilt %v, %v", rebuilt, err)
	}
	if diff := cmp.Diff(wantIDs, entryIDs(idx)); diff != "" {
		t.Errorf("index ids (-want +got):\n%s", diff)
	}
	if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
		t.Errorf("stele.lock rewritten:\n%s", got)
	}
	if got := readGen(t, root); got != want {
		t.Errorf("generated file = %q, want %q", got, want)
	}
}

// git replaces stele.lock, then unlinks stele.md and creates it empty before
// filling it, so a hook can find no rules at all. A write never empties the
// lock: stele compile does that, for a user who means it. Until stele.md lands
// nothing is written, and when it does no rule gets a new id.
func TestEnsureFreshNeverEmptiesTheLock(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	srcs := []source.Source{stelemd.New()}
	stateOf := map[string]func(t *testing.T, root string){
		"missing": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "stele.md")); err != nil {
				t.Fatal(err)
			}
		},
		"empty": func(t *testing.T, root string) { writeRules(t, root, "") },
	}
	for name, empty := range stateOf {
		for _, noIndex := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, no index %v", name, noIndex), func(t *testing.T) {
				root := t.TempDir()
				writeRules(t, root, twoRules)
				if _, err := Compile(ctx, root, srcs, gens, ids(), testKey); err != nil {
					t.Fatal(err)
				}
				// The pull has replaced stele.lock and the rules file.
				rewrite(t, root, func(f *lock.File) { f.Rules[0].ID, f.Rules[1].ID = "pulled-1", "pulled-2" })
				wantIDs := []string{"pulled-1", "pulled-2"}
				writeGen(t, root, wantIDs)
				pulled := lockBytes(t, root)
				want := readGen(t, root)
				if noIndex {
					if err := os.Remove(index.Path(root)); err != nil {
						t.Fatal(err)
					}
				}
				before := fileBytes(t, index.Path(root))
				empty(t, root)

				for hook := 1; hook <= 2; hook++ {
					idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey)
					if err != nil || rebuilt {
						t.Fatalf("hook %d: rebuilt %v, %v", hook, rebuilt, err)
					}
					if !noIndex && len(idx.Rules) != 2 {
						t.Errorf("hook %d: index = %+v, want the one it had", hook, idx)
					}
					if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
						t.Errorf("hook %d: stele.lock rewritten:\n%s", hook, got)
					}
					if got := fileBytes(t, index.Path(root)); !bytes.Equal(got, before) {
						t.Errorf("hook %d: index rewritten", hook)
					}
					if got := readGen(t, root); got != want {
						t.Errorf("hook %d: generated file = %q, want %q", hook, got, want)
					}
				}

				writeRules(t, root, twoRules)
				idx, _, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(wantIDs, entryIDs(idx)); diff != "" {
					t.Errorf("after stele.md: index ids (-want +got):\n%s", diff)
				}
				if got := readGen(t, root); got != want {
					t.Errorf("after stele.md: generated file = %q, want %q", got, want)
				}
				if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
					t.Errorf("after stele.md: stele.lock rewritten:\n%s", got)
				}
			})
		}
	}

	// Only the builtin's rules are gone: an external rule left in the index
	// does not make it a rebuild to apply.
	t.Run("an external rule remains", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, twoRules)
		org := newFake()
		org.rules[0].ID = ""
		if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
			t.Fatal(err)
		}
		pulled := lockBytes(t, root)
		writeRules(t, root, "")
		if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey); err != nil || rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
		if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
			t.Errorf("stele.lock rewritten:\n%s", got)
		}
	})
}

// The guard is for a lock with rules to lose, and for losing all of them.
func TestEnsureFreshStillFollowsStelemd(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	srcs := []source.Source{stelemd.New()}

	t.Run("one rule left", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, twoRules)
		if _, err := Compile(ctx, root, srcs, gens, ids(), testKey); err != nil {
			t.Fatal(err)
		}
		writeRules(t, root, "Every handler goes through the middleware chain.\n")
		idx, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey)
		if err != nil || !rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
		if diff := cmp.Diff([]string{"id-2"}, entryIDs(idx)); diff != "" {
			t.Errorf("index ids (-want +got):\n%s", diff)
		}
		f, err := lock.Load(root)
		if err != nil || len(f.Rules) != 1 {
			t.Errorf("lock = %+v, %v, want the one rule", f, err)
		}
	})

	// The lock holds only an external source's rules: stele.md has none to lose.
	t.Run("no rules and only external ones in the lock", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, "")
		org := newFake()
		org.rules[0].ID = ""
		if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
			t.Fatal(err)
		}
		writeRules(t, root, "\n")
		if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey); err != nil || !rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
	})

	// A new project: no rules yet, in stele.md or in the lock.
	t.Run("no rules and none in the lock", func(t *testing.T) {
		root := t.TempDir()
		writeRules(t, root, "")
		mint := func() string { return "project" }
		if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, mint, testKey); err != nil || !rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
		if _, rebuilt, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey); err != nil || rebuilt {
			t.Errorf("second EnsureFresh: rebuilt %v, %v", rebuilt, err)
		}
	})
}

// A user who removes every rule means it: stele check says the lock is out of
// date, and stele compile empties it.
func TestCompileStillEmptiesTheLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gens := []Generated{idListing}
	srcs := []source.Source{stelemd.New()}
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, srcs, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	writeRules(t, root, "")
	if _, _, err := EnsureFresh(ctx, root, srcs, gens, noMint(t), testKey); err != nil {
		t.Fatal(err)
	}

	r, err := Plan(ctx, root, srcs, gens, noMint(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.LockChanged {
		t.Error("Plan: stele check would not report the lock")
	}

	if _, err := Compile(ctx, root, srcs, gens, noMint(t), testKey); err != nil {
		t.Fatal(err)
	}
	f, err := lock.Load(root)
	if err != nil || len(f.Rules) != 0 {
		t.Errorf("lock = %+v, %v, want no rules", f, err)
	}
	idx, err := index.Load(root, testKey)
	if err != nil || len(idx.Rules) != 0 {
		t.Errorf("index = %+v, %v, want no rules", idx, err)
	}
}

// The hook cannot read an external source, so it cannot tell what a pulled
// lock says about one. It keeps the lock as pulled, every entry of it.
func TestEnsureFreshKeepsPulledExternalEntries(t *testing.T) {
	ctx := context.Background()
	gens := []Generated{idListing}
	for name, change := range map[string]func(*lock.File){
		"reworded": func(f *lock.File) { f.Rules[2].Hash = "reworded-by-a-teammate" },
		"added": func(f *lock.File) {
			f.Rules[2].ID = "pulled-org"
			f.Rules = append(f.Rules, lock.Entry{ID: "org-new", Source: "org", Hash: "h", Position: 1})
		},
		"removed": func(f *lock.File) { f.Rules = f.Rules[:2] },
		"new ids and an entry the index lacks": func(f *lock.File) {
			f.Rules[0].ID = "pulled-1"
			f.Rules = append(f.Rules, lock.Entry{ID: "org-new", Source: "org", Hash: "h", Position: 1})
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRules(t, root, twoRules)
			org := newFake()
			org.rules[0].ID = ""
			if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
				t.Fatal(err)
			}
			rewrite(t, root, change)
			pulled := lockBytes(t, root)
			want := readGen(t, root)

			for hook := 1; hook <= 2; hook++ {
				_, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, func() string { return "thrown-away" }, testKey)
				if err != nil || rebuilt {
					t.Errorf("hook %d: rebuilt %v, %v", hook, rebuilt, err)
				}
				if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
					t.Errorf("hook %d: stele.lock rewritten:\n%s", hook, got)
				}
				if got := readGen(t, root); got != want {
					t.Errorf("hook %d: generated file = %q, want %q", hook, got, want)
				}
			}
		})
	}
}

// An id that moved to the rule of another source, with one body in both:
// every id and hash still pairs up, and only the source tells the lock has
// changed.
func TestEnsureFreshSeesAnIDChangeSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gens := []Generated{idListing}
	writeRules(t, root, "One body in two sources.\n")
	org := newFake()
	org.rules[0].ID = ""
	org.rules[0].Body = "One body in two sources."
	if _, err := Compile(ctx, root, []source.Source{stelemd.New(), org}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	rewrite(t, root, func(f *lock.File) { f.Rules[0].ID, f.Rules[1].ID = f.Rules[1].ID, f.Rules[0].ID })
	writeGen(t, root, []string{"id-2", "id-1"})
	pulled := lockBytes(t, root)

	idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, noMint(t), testKey)
	if err != nil || !rebuilt {
		t.Fatalf("rebuilt %v, %v, want a rebuild", rebuilt, err)
	}
	if diff := cmp.Diff([]string{"id-2", "id-1"}, entryIDs(idx)); diff != "" {
		t.Errorf("index ids (-want +got):\n%s", diff)
	}
	if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
		t.Errorf("stele.lock rewritten:\n%s", got)
	}
}

// unreadable is a builtin whose fingerprint is unchanged and that fails a read.
type unreadable struct{ source.Source }

func (unreadable) Read(context.Context, string) ([]source.Rule, string, error) {
	return nil, "", errors.New("cannot read")
}

// A write never pays for a lock that disagrees: it does not wait for another
// compile, and a plan that fails leaves it with the index it has.
func TestEnsureFreshPulledLockNeverHoldsUpTheHook(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writeRules(t, root, twoRules)
		if _, err := Compile(ctx, root, []source.Source{stelemd.New()}, nil, ids(), testKey); err != nil {
			t.Fatal(err)
		}
		rewrite(t, root, func(f *lock.File) { f.Rules[0].ID = "pulled-1" })
		return root
	}

	t.Run("another compile holds the lock", func(t *testing.T) {
		root := setup(t)
		unlock, err := lockCompile(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		start := time.Now()
		idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, nil, noMint(t), testKey)
		if err != nil || rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
		if d := time.Since(start); d >= lockWait {
			t.Errorf("waited %v for the compile lock", d)
		}
		if diff := cmp.Diff([]string{"id-1", "id-2"}, entryIDs(idx)); diff != "" {
			t.Errorf("index ids (-want +got):\n%s", diff)
		}
	})

	t.Run("the plan fails", func(t *testing.T) {
		root := setup(t)
		pulled := lockBytes(t, root)
		idx, rebuilt, err := EnsureFresh(ctx, root, []source.Source{unreadable{stelemd.New()}}, nil, noMint(t), testKey)
		if err != nil || rebuilt {
			t.Fatalf("rebuilt %v, %v", rebuilt, err)
		}
		if len(idx.Rules) != 2 {
			t.Errorf("index = %+v, want the one it had", idx)
		}
		if got := lockBytes(t, root); !bytes.Equal(got, pulled) {
			t.Errorf("stele.lock rewritten:\n%s", got)
		}
	})
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
