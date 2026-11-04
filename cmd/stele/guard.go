package main

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/scope"
)

// ownNames are stele's own files at any depth: the root's, a subproject's, or
// one a write would create to start a subproject, which would have no rules.
// The agent's project settings count too: they load stele's hooks, and the
// agent may reload them mid-session, so an edit can switch stele off at once
// (#56). So does the rules file the agent loads at launch (#68): an edit
// there changes the rules it reads.
var ownNames = append([]string{"stele.md", enforce.FileName, "stele.lock", ".stele", "**/.stele/"}, agentNames()...)

// agentNames are every agent's settings files and rules file, at any depth:
// whichever agent writes, a change to another's can switch stele off for it.
// A rules file that is the user's own, which stele writes a section of, is
// not: it holds the user's text too, and stele rewrites its section when it
// changes.
func agentNames() []string {
	var names []string
	for _, p := range agentFiles() {
		names = append(names, "**/"+p)
	}
	return names
}

// agentFiles are every agent's settings files and the rules files that are
// stele's own, as slash paths relative to a root.
func agentFiles() []string {
	var files []string
	for _, a := range agents {
		files = append(files, a.Settings()...)
		if !a.SharedRulesFile() {
			files = append(files, a.RulesFile())
		}
	}
	return files
}

// allSettings are every agent's project settings files.
func allSettings() []string {
	var files []string
	for _, a := range agents {
		files = append(files, a.Settings()...)
	}
	return files
}

// guarded reports whether a write to file, rel to root, changes one of
// stele's own files. Names count at any depth, a subproject's too, and so do
// symlinks and junctions on the way, followed everywhere. A hard link or an
// 8.3 short name, which only a same-file test can tell, is caught for the
// root's stele.md, stele.json, stele.lock, .stele/, and the agent's settings
// files and rules file only. A change there can weaken stele or turn it off,
// so the user is asked every time (#52).
func guarded(root, rel, file string) bool {
	if ownName(rel) {
		return true
	}
	if r, ok := safefile.Resolve(file); ok && r != file {
		if _, rrel, ok := target(r); ok && ownName(rrel) {
			return true
		}
	}
	// The root's .stele under another name, such as an 8.3 short name.
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
	// A new settings file in the root's settings directory under another
	// name.
	if info, err := os.Stat(filepath.Dir(file)); err == nil {
		base := cleanRel(filepath.Base(file))
		for _, p := range allSettings() {
			dir, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Dir(p))))
			if err == nil && os.SameFile(info, dir) && strings.EqualFold(base, path.Base(p)) {
				return true
			}
		}
	}
	// The root's own files under another name: a hard link, or an 8.3 short
	// name.
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	names := []string{"stele.md", enforce.FileName, "stele.lock"}
	for _, p := range agentFiles() {
		names = append(names, filepath.FromSlash(p))
	}
	for _, name := range names {
		if own, err := os.Stat(filepath.Join(root, name)); err == nil && os.SameFile(info, own) {
			return true
		}
	}
	return false
}

// ownName reports whether rel, relative to a root, names one of stele's own
// files, after cleanRel.
func ownName(rel string) bool {
	clean := cleanRel(rel)
	for _, p := range ownNames {
		if scope.Match(p, clean) {
			return true
		}
	}
	return false
}

// cleanRel cleans each segment of a slash path as Windows cleans it: trailing
// dots and spaces go, and so does a :stream, so stele.json. and
// stele.md::$DATA reach the real file. The clean-up runs everywhere: an extra
// prompt is harmless.
func cleanRel(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		if j := strings.IndexByte(s, ':'); j >= 0 {
			s = s[:j]
		}
		segs[i] = strings.TrimRight(s, ". ")
	}
	return strings.Join(segs, "/")
}

// sessionSettings reports whether file is a settings file the agent loads
// for the session in cwd, when that session is in a stele project (#56): the
// agent's project settings in each directory from the session's up to the
// top of its git checkout, and the user's settings, which apply everywhere.
func sessionSettings(cwd, file string) bool {
	if cwd == "" {
		return false
	}
	root, _, ok := target(filepath.Join(cwd, "x"))
	if !ok {
		return false
	}
	top := root
	if git, ok := walkUp(root, ".git"); ok {
		top = git
	}
	for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
		if rel, ok := below(d, file); ok {
			clean := cleanRel(rel)
			for _, p := range allSettings() {
				if scope.Match("/"+p, clean) {
					return true
				}
			}
		}
		if d == top || filepath.Dir(d) == d {
			break
		}
	}
	var users []string
	for _, a := range agents {
		users = append(users, a.UserSettings()...)
	}
	for _, u := range users {
		if rel, ok := below(filepath.Dir(u), file); ok && scope.Match("/"+filepath.Base(u), cleanRel(rel)) {
			return true
		}
	}
	return false
}

// loadsSettings reports whether a write to file, or through the links on its
// way, lands on a settings file the session loads.
func loadsSettings(ev adapter.Event, file string) bool {
	if sessionSettings(ev.Cwd, file) {
		return true
	}
	r, ok := safefile.Resolve(file)
	return ok && r != file && sessionSettings(ev.Cwd, r)
}

// below returns file relative to dir, with / separators; ok is false when
// file is not under dir.
func below(dir, file string) (string, bool) {
	rel, err := filepath.Rel(dir, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// askOutside answers a write to e's file outside every stele project: ask when
// it is a settings file the session loads, or a link leads it to one of a
// project's own files (#52, #56), else nothing.
func askOutside(ev adapter.Event, e adapter.Edit) adapter.Answer {
	if !e.Writes && !e.Removes {
		return adapter.Answer{}
	}
	if loadsSettings(ev, e.File) {
		return adapter.Answer{Ask: install.AskMessage(agent.Words(), showPath(filepath.ToSlash(e.File)))}
	}
	r, ok := safefile.Resolve(e.File)
	if !ok || r == e.File {
		return adapter.Answer{}
	}
	if root, rel, ok := target(r); ok && guarded(root, rel, r) {
		return adapter.Answer{Ask: install.AskMessage(agent.Words(), showPath(rel))}
	}
	return adapter.Answer{}
}
