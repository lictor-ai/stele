package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/scope"
)

// ownNames are stele's own files at any depth: the root's, a subproject's, or
// one a write would create to start a subproject, which would have no rules.
var ownNames = []string{"stele.md", enforce.FileName, "stele.lock", ".stele", "**/.stele/"}

// guarded reports whether a write to file, rel to root, changes one of
// stele's own files: by name, through a link, or as the same file under
// another name. A change there can weaken stele or turn it off, so the user
// is asked every time (#52).
func guarded(root, rel, file string) bool {
	if ownName(rel) {
		return true
	}
	if r, ok := resolve(file); ok && r != file {
		if _, rrel, ok := target(claudecode.Event{File: r}); ok && ownName(rrel) {
			return true
		}
	}
	// A directory that is .stele under another name: a junction, which
	// EvalSymlinks does not follow on Windows, or an 8.3 short name.
	if dot, err := os.Stat(filepath.Join(root, ".stele")); err == nil {
		for d := filepath.Dir(file); ; d = filepath.Dir(d) {
			if info, err := os.Stat(d); err == nil && os.SameFile(info, dot) {
				return true
			}
			if d == root || filepath.Dir(d) == d {
				break
			}
		}
	}
	// A hard link, or a name Windows maps to the same file.
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	for _, name := range []string{"stele.md", enforce.FileName, "stele.lock"} {
		if own, err := os.Stat(filepath.Join(root, name)); err == nil && os.SameFile(info, own) {
			return true
		}
	}
	return false
}

// ownName reports whether rel, relative to a root, names one of stele's own
// files. Each segment is first cleaned as Windows cleans it: trailing dots
// and spaces go, and so does a :stream, so stele.json. and stele.md::$DATA
// reach the real file. The clean-up runs everywhere: an extra prompt is
// harmless.
func ownName(rel string) bool {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		if j := strings.IndexByte(s, ':'); j >= 0 {
			s = s[:j]
		}
		segs[i] = strings.TrimRight(s, ". ")
	}
	clean := strings.Join(segs, "/")
	for _, p := range ownNames {
		if scope.Match(p, clean) {
			return true
		}
	}
	return false
}

// askOutside answers a write to file outside every stele project: ask when
// a link leads it to one of a project's own files (#52), else nothing.
func askOutside(ev claudecode.Event) claudecode.Answer {
	if !writes[ev.ToolName] {
		return claudecode.Answer{}
	}
	r, ok := resolve(ev.File)
	if !ok || r == ev.File {
		return claudecode.Answer{}
	}
	if root, rel, ok := target(claudecode.Event{File: r}); ok && guarded(root, rel, r) {
		return claudecode.Answer{Ask: install.AskMessage(showPath(rel))}
	}
	return claudecode.Answer{}
}

// maxHops bounds the links resolve follows, against a loop.
const maxHops = 32

// resolve returns where a write to file lands, following each link on the
// way, a dangling final one included, which a write would create. It follows
// Windows junctions too, which filepath.EvalSymlinks does not. ok is false
// when it cannot tell.
func resolve(file string) (string, bool) {
	p := filepath.Clean(file)
	for hops := 0; ; {
		vol := filepath.VolumeName(p)
		parts := strings.Split(strings.TrimPrefix(p[len(vol):], string(filepath.Separator)), string(filepath.Separator))
		cur := vol + string(filepath.Separator)
		next := ""
		for i, part := range parts {
			step := filepath.Join(cur, part)
			info, err := os.Lstat(step)
			if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
				cur = step // a plain directory, or one the write would create
				continue
			}
			to, err := os.Readlink(step)
			if err != nil {
				cur = step // a reparse point that is not a link
				continue
			}
			if !filepath.IsAbs(to) {
				to = filepath.Join(cur, to)
			}
			next = filepath.Join(append([]string{to}, parts[i+1:]...)...)
			break
		}
		if next == "" {
			return cur, true
		}
		if hops++; hops > maxHops {
			return "", false
		}
		p = filepath.Clean(next)
	}
}
