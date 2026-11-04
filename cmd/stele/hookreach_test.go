package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
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
// this machine, and nothing it runs may reach the network, a process or
// internal/publish. It follows runStats through the declarations of
// cmd/stele it uses, in any file, and the packages those use: stats calls
// showPath in check.go, which imports os/exec for git, and must not count
// as running git.
func TestStatsNeverReachesNetwork(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	imports := declReach(t, "runStats")
	for _, p := range forbidden(imports) {
		t.Errorf("stele stats uses %s", p)
	}
	var start []string
	for p := range imports {
		if strings.HasPrefix(p, module+"/") {
			start = append(start, packageFiles(t, filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(p, module+"/"))))...)
		}
	}
	for _, p := range forbidden(reached(t, repo, start)) {
		t.Errorf("stele stats reaches %s", p)
	}
}

// The walk sees through files: a declaration's own imports count, its
// file's others do not.
func TestDeclReach(t *testing.T) {
	imports := declReach(t, "runCheck")
	if !imports["os/exec"] {
		t.Errorf("runCheck reaches %v, want os/exec through gitBin", imports)
	}
	if imports := declReach(t, "showPath"); imports["os/exec"] {
		t.Errorf("showPath reaches %v, want no os/exec", imports)
	}
}

// declReach returns the packages that entry, a declaration of cmd/stele,
// uses: through every package-level declaration it names, in any non-test
// file, and the methods of every type it names. A local name that shadows a
// declaration counts it too, which errs toward reaching more.
func declReach(t *testing.T, entry string) map[string]bool {
	t.Helper()
	type decl struct {
		node    ast.Node
		imports map[string]string // name → path, of its file
	}
	decls := map[string][]decl{}
	methods := map[string][]decl{}
	fset := token.NewFileSet()
	for _, name := range packageFiles(t, ".") {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		imps := map[string]string{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			n := path.Base(p)
			if imp.Name != nil {
				n = imp.Name.Name
			}
			imps[n] = p
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) == 1 {
					typ := d.Recv.List[0].Type
					if star, ok := typ.(*ast.StarExpr); ok {
						typ = star.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						methods[id.Name] = append(methods[id.Name], decl{d, imps})
					}
					continue
				}
				decls[d.Name.Name] = append(decls[d.Name.Name], decl{d, imps})
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						decls[s.Name.Name] = append(decls[s.Name.Name], decl{s, imps})
					case *ast.ValueSpec:
						for _, n := range s.Names {
							decls[n.Name] = append(decls[n.Name], decl{s, imps})
						}
					}
				}
			}
		}
	}
	if len(decls[entry]) == 0 {
		t.Fatalf("no declaration %s in cmd/stele", entry)
	}
	used := map[string]bool{}
	seen := map[string]bool{entry: true}
	queue := append([]decl{}, decls[entry]...)
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		ast.Inspect(d.node, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if p, ok := d.imports[id.Name]; ok {
						used[p] = true
					}
				}
			}
			if id, ok := n.(*ast.Ident); ok && !seen[id.Name] {
				if ds, ok := decls[id.Name]; ok {
					seen[id.Name] = true
					queue = append(queue, ds...)
					queue = append(queue, methods[id.Name]...)
				}
			}
			return true
		})
	}
	return used
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
