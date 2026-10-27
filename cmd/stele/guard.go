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
// Claude Code's project settings count too: they load stele's hooks, and
// Claude Code reloads them mid-session, so an edit can switch stele off at
// once (#56). So does the rules file the agent loads at launch (#68): an edit
// there changes the rules it reads.
var ownNames = []string{
	"stele.md", enforce.FileName, "stele.lock", ".stele", "**/.stele/",
	"**/.claude/settings.json", "**/.claude/settings.local.json",
	"**/" + claudecode.RulesFile,
}

// settingsNames are the files in .claude that Claude Code loads settings from.
var settingsNames = []string{"settings.json", "settings.local.json"}

// guarded reports whether a write to file, rel to root, changes one of
// stele's own files. Names count at any depth, a subproject's too, and so do
// symlinks and junctions on the way, followed everywhere. A hard link or an
// 8.3 short name, which only a same-file test can tell, is caught for the
// root's stele.md, stele.json, stele.lock, .stele/, .claude settings files
// and rules file only. A change there
// can weaken stele or turn it off, so the user is asked every time (#52).
func guarded(root, rel, file string) bool {
	if ownName(rel) {
		return true
	}
	if r, ok := resolve(file); ok && r != file {
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
	// A new settings file in the root's .claude under another name.
	if dot, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		if info, err := os.Stat(filepath.Dir(file)); err == nil && os.SameFile(info, dot) {
			base := cleanRel(filepath.Base(file))
			for _, name := range settingsNames {
				if strings.EqualFold(base, name) {
					return true
				}
			}
		}
	}
	// The root's own files under another name: a hard link, or an 8.3 short
	// name.
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	for _, name := range []string{"stele.md", enforce.FileName, "stele.lock",
		claudecode.SettingsFile, filepath.Join(".claude", "settings.local.json"),
		filepath.FromSlash(claudecode.RulesFile)} {
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

// sessionSettings reports whether file is a settings file Claude Code loads
// for the session in ev.Cwd, when that session is in a stele project (#56):
// .claude/settings.json and settings.local.json in each directory from the
// session's up to the top of its git checkout (settings.local.json sits at
// the git root), and the user's settings.json, which applies everywhere.
func sessionSettings(ev claudecode.Event, file string) bool {
	if ev.Cwd == "" {
		return false
	}
	root, _, ok := target(filepath.Join(ev.Cwd, "x"))
	if !ok {
		return false
	}
	top := root
	if git, ok := walkUp(root, ".git"); ok {
		top = git
	}
	for d := filepath.Clean(ev.Cwd); ; d = filepath.Dir(d) {
		if rel, ok := below(d, file); ok {
			clean := cleanRel(rel)
			for _, name := range settingsNames {
				if scope.Match("/.claude/"+name, clean) {
					return true
				}
			}
		}
		if d == top || filepath.Dir(d) == d {
			break
		}
	}
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		cfg = filepath.Join(home, ".claude")
	}
	cfg, err := filepath.Abs(cfg) // a relative CLAUDE_CONFIG_DIR is the process's
	if err != nil {
		return false
	}
	rel, ok := below(cfg, file)
	return ok && scope.Match("/settings.json", cleanRel(rel))
}

// loadsSettings reports whether a write to file, or through the links on its
// way, lands on a settings file the session loads.
func loadsSettings(ev claudecode.Event, file string) bool {
	if sessionSettings(ev, file) {
		return true
	}
	r, ok := resolve(file)
	return ok && r != file && sessionSettings(ev, r)
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

// askOutside answers a write to file outside every stele project: ask when
// it is a settings file the session loads, or a link leads it to one of a
// project's own files (#52, #56), else nothing.
func askOutside(ev claudecode.Event) claudecode.Answer {
	if !ev.Writes {
		return claudecode.Answer{}
	}
	if loadsSettings(ev, ev.File) {
		return claudecode.Answer{Ask: install.AskMessage(showPath(filepath.ToSlash(ev.File)))}
	}
	r, ok := resolve(ev.File)
	if !ok || r == ev.File {
		return claudecode.Answer{}
	}
	if root, rel, ok := target(r); ok && guarded(root, rel, r) {
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
