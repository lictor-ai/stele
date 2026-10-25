package compile

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// owned is the listing, stele's only when it starts with "# generated".
var owned = Generated{
	Path: listing.Path,
	Render: func(idx index.Index) []byte {
		return append([]byte("# generated v1\n"), listing.Render(idx)...)
	},
	Owned: func(disk []byte) bool { return strings.HasPrefix(string(disk), "# generated") },
	// The header line is words, not rules.
	Same: func(disk, want []byte) bool {
		_, a, _ := strings.Cut(string(disk), "\n")
		_, b, _ := strings.Cut(string(want), "\n")
		return strings.HasPrefix(string(disk), "# generated") && a == b
	},
}

func TestGeneratedNotOursIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	path := filepath.Join(root, "gen", "rules.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("the user's own file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Compile(ctx, root, []source.Source{stelemd.New()}, []Generated{owned}, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Files[0].Foreign || r.Files[0].Changed {
		t.Errorf("Files = %+v, want foreign and unchanged", r.Files)
	}
	if got := readGen(t, root); got != "the user's own file\n" {
		t.Errorf("compile overwrote it: %q", got)
	}
	if _, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, []Generated{owned}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	if got := readGen(t, root); got != "the user's own file\n" {
		t.Errorf("EnsureFresh overwrote it: %q", got)
	}
}

func TestGeneratedSameIgnoresWords(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	if _, err := Compile(ctx, root, []source.Source{stelemd.New()}, []Generated{owned}, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gen", "rules.txt")
	body := strings.SplitN(readGen(t, root), "\n", 2)[1]
	if err := os.WriteFile(path, []byte("# generated v0, older words\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Plan(ctx, root, []source.Source{stelemd.New()}, []Generated{owned}, ids())
	if err != nil {
		t.Fatal(err)
	}
	if r.Files[0].Changed {
		t.Error("a header change alone planned as stale")
	}
}

// On the hook's path a stale file is refreshed when that is cheap, and
// skipped otherwise: never a wait, never an error.
func TestEnsureFreshRefreshIsBestEffort(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeRules(t, root, twoRules)
	gens := []Generated{listing}
	if _, err := Compile(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gen", "rules.txt")

	t.Run("lock held", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		unlock, err := safefile.Lock(filepath.Join(root, index.Dir, lockName), time.Second, lockStale)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		start := time.Now()
		if _, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
			t.Errorf("EnsureFresh with the lock held = %v, want a skip", err)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("EnsureFresh waited %v for the lock", d)
		}
	})

	t.Run("read-only file", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			// A rename replaces a read-only file on Unix: nothing to test.
			t.Skip("read-only blocks a replace only on Windows")
		}
		if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o444); err != nil {
			t.Skipf("cannot make it read-only: %v", err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o644) })
		start := time.Now()
		if _, _, err := EnsureFresh(ctx, root, []source.Source{stelemd.New()}, gens, ids(), testKey); err != nil {
			t.Errorf("EnsureFresh = %v, want a skip", err)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("EnsureFresh took %v on a read-only file", d)
		}
	})
}
