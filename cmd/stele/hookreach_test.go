package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// module is this repository's module path.
const module = "github.com/lictor-ai/stele"

// TestHookNeverReachesPublish guards D45 and D66: stele hook runs on every
// write, so nothing it imports may reach internal/publish, the network or a
// process. It follows the imports of cmd/stele/hook*.go through the
// repository's own packages.
func TestHookNeverReachesPublish(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	hookFiles, err := filepath.Glob("hook*.go")
	if err != nil {
		t.Fatal(err)
	}
	var start []string
	for _, f := range hookFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		start = append(start, f)
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"runPublish", "publish."} {
			if strings.Contains(string(src), word) {
				t.Errorf("%s mentions %s", f, word)
			}
		}
	}
	if len(start) == 0 {
		t.Fatal("no hook*.go files found")
	}

	for _, p := range forbidden(reached(t, repo, start)) {
		t.Errorf("stele hook reaches %s", p)
	}
}

// TestStatsNeverReachesNetwork guards D70: stele stats reads the log on
// this machine, and nothing it imports may reach the network, a process or
// internal/publish.
func TestStatsNeverReachesNetwork(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range forbidden(reached(t, repo, []string{"stats.go"})) {
		t.Errorf("stele stats reaches %s", p)
	}
}

// reached is every package the files import, through the repository's own
// packages.
func reached(t *testing.T, repo string, start []string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	var queue []string
	add := func(files []string) {
		for _, f := range files {
			imports, err := fileImports(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range imports {
				if !seen[p] {
					seen[p] = true
					queue = append(queue, p)
				}
			}
		}
	}
	add(start)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if !strings.HasPrefix(p, module+"/") {
			continue
		}
		dir := filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(p, module+"/")))
		add(packageFiles(t, dir))
	}
	return seen
}

// forbidden are the packages in seen that reach the network or a process.
func forbidden(seen map[string]bool) []string {
	var out []string
	for p := range seen {
		switch {
		case p == module+"/internal/publish",
			p == "net", strings.HasPrefix(p, "net/"),
			p == "os/exec":
			out = append(out, p)
		}
	}
	return out
}

// fileImports returns the import paths of one Go file.
func fileImports(path string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// packageFiles lists the non-test Go files of the package in dir.
func packageFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}
