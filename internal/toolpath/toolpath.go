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

// GitFlags returns the flags that go before every git command stele runs,
// stele check's and stele publish's, a new slice each time. They stop a
// repository's own config starting a program. What only matters to a remote,
// such as which transports may be used, is stele publish's: stele check
// reaches no remote but the promisor of a partial clone, which the user chose.
func GitFlags() []string {
	return []string{
		"-c", "core.fsmonitor=false",
		// A bare repository is used only when named with --git-dir: a cloned
		// project laid out as one could otherwise bring its own config, with
		// filters and hooks that run commands. git before 2.38 ignores this.
		"-c", "safe.bareRepository=explicit",
	}
}

// Vet returns the absolute path of found, the program name was found at on
// PATH. A program inside the project root, or inside the git repository that
// holds it (see repoTop), is ErrRefused, as D43 refuses plugins from inside
// the repository.
func Vet(name, found, root string) (string, error) {
	abs, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if newProject(root).holds(abs) {
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
	p := newProject(root)
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if !isPath(name) {
			out = append(out, kv)
		} else if kept := p.outside(value); kept != "" {
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
// relative or inside the project, in order. Windows quotes an entry
// that holds the separator; SplitList takes the quotes off, so they go back.
// git ignores the quotes and splits on every separator, so each part of an
// entry must pass, or the whole entry goes.
func (p *project) outside(list string) string {
	sep := string(os.PathListSeparator)
	var kept []string
next:
	for _, e := range filepath.SplitList(list) {
		for _, part := range strings.Split(e, sep) {
			if !filepath.IsAbs(part) || p.holds(part) {
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

// project is the directories that count as inside, worked out once: a PATH
// has many entries, and each would otherwise repeat it.
type project struct {
	names []string        // each directory, as spelled and as it resolves
	ids   []os.FileInfo   // each directory, for os.SameFile
	known map[string]bool // what under found, by directory
}

func newProject(root string) *project {
	p := &project{known: map[string]bool{}}
	for _, d := range projectDirs(root) {
		p.names = append(p.names, d)
		if abs, err := filepath.Abs(d); err == nil {
			p.names = append(p.names, abs)
		}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			p.names = append(p.names, r)
		}
		if info, err := os.Stat(d); err == nil {
			p.ids = append(p.ids, info)
		}
	}
	return p
}

// holds reports whether path is inside the project, by name or by being the
// same directory, as it stands or where it leads: a path has many spellings,
// and a junction or a link leads out of the text of its path. A path whose
// links cannot be followed counts as inside.
func (p *project) holds(path string) bool {
	target, ok := safefile.Resolve(path)
	if !ok {
		return true
	}
	for _, x := range []string{path, target} {
		for _, name := range p.names {
			if within(x, name) {
				return true
			}
		}
		if p.under(x) {
			return true
		}
	}
	return false
}

// under reports whether d or a parent of d is one of the project's
// directories. A directory that does not exist is judged by its parents.
func (p *project) under(d string) bool {
	if v, ok := p.known[d]; ok {
		return v
	}
	v := false
	if info, err := os.Stat(d); err == nil {
		for _, id := range p.ids {
			v = v || os.SameFile(info, id)
		}
	}
	if parent := filepath.Dir(d); !v && parent != d {
		v = p.under(parent)
	}
	p.known[d] = v
	return v
}

// projectDirs are root and the repository that holds it. That is the nearest
// repository to where root leads, not to how it is spelled: a link to a
// project in another repository sits in a repository that is not its own. A
// root that cannot be resolved is taken as spelled. Only the nearest counts,
// so a superproject's folders are not inside.
func projectDirs(root string) []string {
	var dirs []string
	if root != "" {
		dirs = append(dirs, root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return dirs
	}
	if r, ok := safefile.Resolve(abs); ok {
		abs = r
	}
	if top := repoTop(abs); top != "" {
		dirs = append(dirs, top)
	}
	return dirs
}

// repoTop is the nearest directory at or above dir, an absolute path, that
// holds .git, found without running git, or "". stele.json can sit in a
// subfolder, and a tool anywhere in the repository came with the clone. A
// repository at the home directory or a filesystem root is "": ~/.git holds
// dotfiles and /.git a container's image, and neither came with this project,
// while ~/bin and /usr/bin hold the user's own programs.
func repoTop(dir string) string {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if homeOrFSRoot(dir) {
				return ""
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// homeOrFSRoot reports whether dir is a filesystem root or, by file identity,
// the user's home directory.
func homeOrFSRoot(dir string) bool {
	if filepath.Dir(dir) == dir {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	a, err := os.Stat(dir)
	if err != nil {
		return false
	}
	b, err := os.Stat(home)
	return err == nil && os.SameFile(a, b)
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
