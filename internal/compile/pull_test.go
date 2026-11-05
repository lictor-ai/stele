package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// pulledPin sets up the case of a pull that moved a pin: developer A and B
// compiled at pinA; A then pinned pinB, which brings a new shared rule, and
// B pulled A's stele.lock and rules file. B's index is still at pinA. It
// returns B's root and A's lock and rules file.
func pulledPin(t *testing.T) (b string, aLock, aGen []byte) {
	t.Helper()
	ctx := context.Background()
	a, b := t.TempDir(), t.TempDir()
	for _, root := range []string{a, b} {
		writeRules(t, root, twoRules)
		if _, err := Compile(ctx, root, []source.Source{stelemd.New(), shared(pinA)}, []Generated{listing}, ids(), testKey); err != nil {
			t.Fatal(err)
		}
	}
	// B's lock must be A's to begin with, as after a pull: copy it.
	copyFile(t, filepath.Join(a, lock.FileName), filepath.Join(b, lock.FileName))
	if _, err := Compile(ctx, b, []source.Source{stelemd.New(), shared(pinA)}, []Generated{listing}, ids(), testKey); err != nil {
		t.Fatal(err)
	}

	// The new pin keeps the old shared rule as it was and adds one: B's
	// index then agrees with every entry it holds, and only lacks the new.
	moved := shared(pinB)
	moved.more = []string{"Shared: name the owner of every goroutine."}
	if _, err := Compile(ctx, a, []source.Source{stelemd.New(), moved}, []Generated{listing}, func() string { return "minted-in-a" }, testKey); err != nil {
		t.Fatal(err)
	}
	aLock = lockBytes(t, a)
	aGen = []byte(readGen(t, a))
	copyFile(t, filepath.Join(a, lock.FileName), filepath.Join(b, lock.FileName))
	copyFile(t, filepath.Join(a, "gen", "rules.txt"), filepath.Join(b, "gen", "rules.txt"))
	return b, aLock, aGen
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// After the pull, B's index holds the old shared rules: the hook must not
// write them over the pulled rules file.
func TestEnsureFreshAfterAPulledPinKeepsTheRulesFile(t *testing.T) {
	b, aLock, aGen := pulledPin(t)
	if _, _, err := EnsureFresh(context.Background(), b, []source.Source{stelemd.New()}, []Generated{listing}, noMint(t), testKey); err != nil {
		t.Fatal(err)
	}
	if got := readGen(t, b); got != string(aGen) {
		t.Errorf("rules file after the hook:\n%s\nwant the pulled one:\n%s", got, aGen)
	}
	if got := lockBytes(t, b); string(got) != string(aLock) {
		t.Errorf("stele.lock after the hook:\n%s\nwant the pulled one:\n%s", got, aLock)
	}
}

// The same pull, then a local edit of stele.md: the hook rebuilds, and
// must keep the pulled shared entries and pin exactly.
func TestEnsureFreshAfterAPulledPinAndAnEditKeepsSharedEntries(t *testing.T) {
	b, aLock, aGen := pulledPin(t)
	writeRules(t, b, twoRules+"\n---\n\nPrefer table-driven tests.\n")
	if _, _, err := EnsureFresh(context.Background(), b, []source.Source{stelemd.New()}, []Generated{listing}, func() string { return "new-local" }, testKey); err != nil {
		t.Fatal(err)
	}
	pulled, err := lock.Decode(aLock)
	if err != nil {
		t.Fatal(err)
	}
	got, err := lock.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Commit != pinB {
		t.Errorf("pins = %+v, want the pulled %s", got.Sources, pinB)
	}
	shared := func(f lock.File) []lock.Entry {
		var out []lock.Entry
		for _, e := range f.Rules {
			if strings.HasPrefix(e.Source, "git:") {
				out = append(out, e)
			}
		}
		return out
	}
	if a, g := shared(pulled), shared(got); len(a) != len(g) || len(a) == 0 || a[0] != g[0] {
		t.Errorf("shared entries = %+v, want the pulled %+v", g, a)
	}
	if got := readGen(t, b); got != string(aGen) {
		t.Errorf("rules file rewritten from a stale index:\n%s\nwant\n%s", got, aGen)
	}
}
