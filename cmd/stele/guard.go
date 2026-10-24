package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/enforce"
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

// resolve returns where a write to file lands, following links: of the file
// itself when it exists, else of its directory, then of a dangling final link,
// which a write would create. ok is false when it cannot tell.
func resolve(file string) (string, bool) {
	if r, err := filepath.EvalSymlinks(file); err == nil {
		return r, true
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return "", false
	}
	p := filepath.Join(dir, filepath.Base(file))
	for range 8 {
		to, err := os.Readlink(p)
		if err != nil {
			return p, true // not a link: the write lands here
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(p), to)
		}
		p = filepath.Clean(to)
	}
	return "", false
}
