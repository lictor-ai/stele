package adapter_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Only an adapter's own package and the registries name an adapter
// (ADR-0009): cmd/stele/agents.go, and internal/install/agent_test.go, the
// install tests, which check the real adapter's output.
func TestOnlyTheRegistryImportsAnAdapter(t *testing.T) {
	const module = "github.com/lictor-ai/stele/internal/adapter/"
	root := filepath.Join("..", "..")
	allowed := map[string]bool{"cmd/stele/agents.go": true, "internal/install/agent_test.go": true}
	sawRegistry := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// .git, and .claude/worktrees, which hold whole checkouts.
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		sawRegistry = sawRegistry || rel == "cmd/stele/agents.go"
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			pkg, ok := strings.CutPrefix(path, module)
			if !ok {
				continue
			}
			pkg, _, _ = strings.Cut(pkg, "/")
			if !allowed[rel] && !strings.HasPrefix(rel, "internal/adapter/"+pkg+"/") {
				t.Errorf("%s imports %s: only internal/adapter/%s and cmd/stele/agents.go may (ADR-0009)", rel, path, pkg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRegistry {
		t.Fatal("cmd/stele/agents.go not found: the walk did not reach the module's files")
	}
}
