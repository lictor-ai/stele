// Package toolpath keeps the programs stele starts outside the project. A
// cloned repository can bring its own git, or an ssh for git to find, and a
// PATH entry such as ./bin, or one a tool like direnv adds, would otherwise
// run it. GitFlags stops a repository's config doing the same. This package
// looks at paths and flags, and starts nothing.
package toolpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lictor-ai/stele/internal/safefile"
)

// ErrRefused is a program found inside the project: a cloned repository could
// have put it there.
var ErrRefused = errors.New("refused: it is inside the project")

// GitFlags returns the flags that go before every git command stele runs, a
// new slice each time. Only https and ssh may be used, so a remote can never
// run a transport command (ext::) or reach a local path; credential helpers
// never prompt; submodules are never fetched or pushed; no fsmonitor program
// runs.
func GitFlags() []string {
	return []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=always",
		"-c", "credential.interactive=never",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "push.recurseSubmodules=no",
		"-c", "core.fsmonitor=false",
		// A bare repository is used only when named with --git-dir: a cloned
		// project laid out as one could otherwise bring its own config, with
		// filters and hooks that run commands.
		"-c", "safe.bareRepository=explicit",
	}
}

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
// git ignores the quotes and splits on every separator, so each part of an
// entry must pass, or the whole entry goes.
func outside(list, root string) string {
	sep := string(os.PathListSeparator)
	var kept []string
next:
	for _, e := range filepath.SplitList(list) {
		for _, part := range strings.Split(e, sep) {
			if !filepath.IsAbs(part) || inside(part, root) {
				continue next
			}
		}
		if strings.Contains(e, sep) {
			e = `"` + e + `"`
		}
		kept = append(kept, e)
	}
	return strings.Join(kept, sep)
}

// inside reports whether p is inside root or the git repository that holds
// it, by name or by being the same directory: a path has many spellings, and
// a junction or a link leads out of the text of its path.
func inside(p, root string) bool {
	for _, d := range projectDirs(root) {
		if insideAny(p, d) || under(p, d) {
			return true
		}
	}
	return false
}

// projectDirs are root and the repository that holds it, which is the nearest
// one to root or to where root leads: a root reached through a link has the
// repository only at its target. Only the nearest counts: a dotfiles
// repository in the home directory must not take ~/bin into every project.
func projectDirs(root string) []string {
	var dirs []string
	add := func(d string) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	add(root)
	add(repoTop(root))
	if abs, err := filepath.Abs(root); err == nil {
		if r, ok := safefile.Resolve(abs); ok && r != abs {
			add(repoTop(r))
		}
	}
	return dirs
}

// under reports whether dir, by file identity, is p or a parent of p, as p
// stands or where it leads. An entry that does not exist yet is judged by its
// parents. A path whose links cannot be followed counts as under dir.
func under(p, dir string) bool {
	want, err := os.Stat(dir)
	if err != nil {
		return false
	}
	target, ok := safefile.Resolve(p)
	if !ok {
		return true
	}
	for _, x := range []string{p, target} {
		for d := x; ; d = filepath.Dir(d) {
			if info, err := os.Stat(d); err == nil && os.SameFile(info, want) {
				return true
			}
			if filepath.Dir(d) == d {
				break
			}
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
