// Package toolpath keeps the programs stele starts outside the project. A
// cloned repository can bring its own git, or an ssh for git to find, and a
// PATH entry such as ./bin, or one a tool like direnv adds, would otherwise
// run it. It decides from paths alone, and starts nothing.
package toolpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrRefused is a program found inside the project: a cloned repository could
// have put it there.
var ErrRefused = errors.New("refused: it is inside the project")

// Vet returns the absolute path of found, the program name was found at on
// PATH. A program inside the project root, or inside the git repository that
// holds it, is ErrRefused, as D43 refuses plugins from inside the repository.
func Vet(name, found, root string) (string, error) {
	abs, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if inside(abs, root) {
		return "", fmt.Errorf("%s at %s: %w", name, abs, ErrRefused)
	}
	return abs, nil
}

// Env returns env for a program stele starts, with PATH cut down to entries
// that cannot be inside the project at root: no empty or relative entry, and
// none inside root or the git repository that holds it. A program vetted with
// Vet still finds what it starts, git its ssh, on PATH. With nothing left the
// variable goes: an empty PATH can mean the current directory. An empty root
// returns env as it is.
func Env(env []string, root string) []string {
	if root == "" {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if !isPath(name) {
			out = append(out, kv)
		} else if kept := outside(value, root); kept != "" {
			out = append(out, name+"="+kept)
		}
	}
	return out
}

// isPath reports whether name is PATH, which Windows spells in any case.
func isPath(name string) bool {
	return name == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(name, "PATH")
}

// outside returns the PATH value list without the entries that are empty,
// relative or inside the project at root, in order. Windows quotes an entry
// that holds the separator; SplitList takes the quotes off, so they go back.
func outside(list, root string) string {
	sep := string(os.PathListSeparator)
	var kept []string
	for _, e := range filepath.SplitList(list) {
		if !filepath.IsAbs(e) || inside(e, root) {
			continue
		}
		if strings.Contains(e, sep) {
			e = `"` + e + `"`
		}
		kept = append(kept, e)
	}
	return strings.Join(kept, sep)
}

// inside reports whether p is inside root or the git repository that holds
// it.
func inside(p, root string) bool {
	for _, r := range []string{root, repoTop(root)} {
		if r != "" && insideAny(p, r) {
			return true
		}
	}
	return false
}

// repoTop is the nearest directory at or above root that holds .git, found
// without running git, or "". stele.json can sit in a subfolder, and a tool
// anywhere in the repository came with the clone.
func repoTop(root string) string {
	dir, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// insideAny reports whether p, or what it links to, is inside root or what
// root links to.
func insideAny(p, root string) bool {
	roots := []string{root}
	if r, err := filepath.Abs(root); err == nil {
		roots = append(roots, r)
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, r)
	}
	paths := []string{p}
	if e, err := filepath.EvalSymlinks(p); err == nil {
		paths = append(paths, e)
	}
	for _, x := range paths {
		for _, r := range roots {
			if within(x, r) {
				return true
			}
		}
	}
	return false
}

// within reports whether p is root or below it. Windows paths compare
// without case.
func within(p, root string) bool {
	if runtime.GOOS == "windows" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
