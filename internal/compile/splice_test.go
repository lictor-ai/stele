package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// A file compile writes only a part of keeps every other byte: Splice puts
// the rendered part in, and refusing is a foreign file, left alone.
func TestCompileSplicesGenerated(t *testing.T) {
	root := t.TempDir()
	writeRules(t, root, twoRules)
	const begin, end = "<<<\n", ">>>\n"
	g := listing
	g.Splice = func(disk, rendered []byte) ([]byte, bool) {
		s := string(disk)
		if strings.Contains(s, "broken") {
			return nil, false
		}
		i, j := strings.Index(s, begin), strings.Index(s, end)
		if i < 0 || j < i {
			return []byte(s + begin + string(rendered) + end), true
		}
		return []byte(s[:i+len(begin)] + string(rendered) + s[j:]), true
	}
	srcs := []source.Source{stelemd.New()}
	path := filepath.Join(root, "gen", "rules.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Compile(context.Background(), root, srcs, []Generated{g}, ids(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	want := "mine\n<<<\nNever build SQL by string concatenation.\nEvery handler goes through the middleware chain.\n>>>\n"
	if got := readGen(t, root); got != want || !r.Files[0].Changed {
		t.Fatalf("file = %q (changed %v), want %q", got, r.Files[0].Changed, want)
	}
	// Up to date: nothing to write.
	r, err = Compile(context.Background(), root, srcs, []Generated{g}, ids(), testKey)
	if err != nil || r.Files[0].Changed {
		t.Fatalf("second compile: %v, changed %v", err, r.Files[0].Changed)
	}
	// Refused: foreign, untouched.
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Compile(context.Background(), root, srcs, []Generated{g}, ids(), testKey)
	if err != nil || !r.Files[0].Foreign {
		t.Fatalf("refused splice: %v, %+v", err, r.Files[0])
	}
	if got := readGen(t, root); got != "broken\n" {
		t.Errorf("foreign file rewritten: %q", got)
	}
}
