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
	"github.com/lictor-ai/stele/internal/sign"
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
// so the user is asked every time (#52). So is a write to a .git, or into
// one, below the root, under any name: one git recognises ends the project.
// And so is a write to stele's signing key, wherever it is (#152).
func guarded(root, rel, file string) bool {
	if ownName(rel) || gitName(root, rel) || inGit(root, file) {
		return true
	}
	r := resolved(file)
	if r != "" {
		if rroot, rrel, ok := target(r); ok && (ownName(rrel) || gitName(rroot, rrel)) {
			return true
		}
	}
	if signingKey(file, r) != keyMissed {
		return true
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

// gitName reports whether rel, relative to root, is a .git or inside one,
// after cleanRel. Inside the root's own repository is left out: stele.md or
// stele.json beside it keeps the project, and git writes there all the time.
func gitName(root, rel string) bool {
	segs := strings.Split(cleanRel(rel), "/")
	for i, s := range segs {
		if !scope.Match("/.git", s) {
			continue
		}
		if i == 0 && len(segs) > 1 && holdsGit(root) {
			continue
		}
		return true
	}
	return false
}

// inGit reports whether file, or a directory on its way below root, is a
// .git under another name, such as an 8.3 short name: the same file as the
// .git beside it. Inside the root's own repository is left out, as gitName
// leaves it.
func inGit(root, file string) bool {
	rootGit := root != "" && holdsGit(root)
	for d := file; d != root && filepath.Dir(d) != d; d = filepath.Dir(d) {
		info, err := os.Stat(d)
		if err != nil {
			continue
		}
		git, err := os.Stat(filepath.Join(filepath.Dir(d), ".git"))
		if err != nil || !os.SameFile(info, git) {
			continue
		}
		if rootGit && filepath.Dir(d) == root && d != file {
			continue
		}
		return true
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
// it is a settings file the session loads, stele's signing key (#152), or a
// link leads it to one of a project's own files (#52, #56), else nothing.
func askOutside(ev adapter.Event, e adapter.Edit) adapter.Answer {
	if !e.Writes && !e.Removes {
		return adapter.Answer{}
	}
	if loadsSettings(ev, e.File) {
		return adapter.Answer{Ask: install.AskMessage(agent.Words(), showPath(filepath.ToSlash(e.File)))}
	}
	r := resolved(e.File)
	switch signingKey(e.File, r) {
	case keyItself:
		return adapter.Answer{Ask: install.AskMessage(agent.Words(), showPath(filepath.ToSlash(e.File)))}
	case keyAbove:
		return adapter.Answer{Ask: install.AskKeyFolderMessage(agent.Words(), showPath(filepath.ToSlash(e.File)))}
	}
	if r == "" {
		return adapter.Answer{}
	}
	if root, rel, ok := target(r); ok && guarded(root, rel, r) {
		return adapter.Answer{Ask: install.AskMessage(agent.Words(), showPath(rel))}
	}
	return adapter.Answer{}
}

// keyHit is what a write does to stele's signing key.
type keyHit int

const (
	keyMissed keyHit = iota // not the key
	keyItself               // the key, or a path below it
	keyAbove                // the key's folder, or one above it
)

// keySide is what signingKey needs of the key, found once per hook call.
type keySide struct {
	dir    string
	base   string        // the key file's name
	names  []string      // the key, as written and after links, as norm writes it
	info   os.FileInfo   // the key; nil when there is none yet
	folder os.FileInfo   // the key's folder; nil when there is none yet
	above  []os.FileInfo // the key's folder and each one above it that exists
}

// keyCache holds the key side for keyDir's dir; runHook empties it, so each
// hook call looks once.
var keyCache *keySide

// loadKeySide returns the key side, nil when there is no config directory.
func loadKeySide() *keySide {
	dir, err := keyDir()
	if err != nil || dir == "" {
		return nil
	}
	if keyCache != nil && keyCache.dir == dir {
		return keyCache
	}
	key := sign.KeyFile(dir)
	k := &keySide{dir: dir, base: filepath.Base(key)}
	for _, n := range withLinks(key) {
		k.names = append(k.names, norm(n))
	}
	if info, err := os.Stat(key); err == nil {
		k.info = info
	}
	if info, err := os.Stat(dir); err == nil {
		k.folder = info
	}
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Stat(d); err == nil {
			k.above = append(k.above, info)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	keyCache = k
	return k
}

// resolved is file after the links on its way, or "" when they lead nowhere
// else.
func resolved(file string) string {
	if r, ok := safefile.Resolve(file); ok && r != file {
		return r
	}
	return ""
}

// signingKey reports what a write to file, or a removal of it, does to the
// key stele signs its files and log with (#152): it changes the key when it
// is the key file or a path below it, and removes or replaces it when it is
// the key's folder or one above it, such as stele's config directory. With
// the key, the agent could sign lines stele never wrote; with a new one,
// stele ignores every line signed before. r is file after its links, as
// resolved returns it. Names are compared as norm writes them, as written
// and after links; a hard link or an 8.3 short name is caught by a same-file
// test. Reading the key is not a write: stele sees no read.
func signingKey(file, r string) keyHit {
	k := loadKeySide()
	if k == nil || file == "" {
		return keyMissed
	}
	fs := []string{norm(file)}
	if r != "" {
		fs = append(fs, norm(r))
	}
	for _, f := range fs {
		for _, n := range k.names {
			switch {
			case f == n || strings.HasPrefix(f, n+"/"):
				return keyItself
			case strings.HasPrefix(n, strings.TrimRight(f, "/")+"/"):
				return keyAbove
			}
		}
	}
	if info, err := os.Stat(file); err == nil {
		if k.info != nil && os.SameFile(info, k.info) {
			return keyItself // the key under another name
		}
		if info.IsDir() {
			for _, a := range k.above {
				if os.SameFile(info, a) {
					return keyAbove // the key's folder under another name
				}
			}
		}
	}
	// A new key in the key's folder under another name.
	if k.folder != nil && strings.EqualFold(cleanRel(filepath.Base(file)), k.base) {
		if parent, err := os.Stat(filepath.Dir(file)); err == nil && os.SameFile(parent, k.folder) {
			return keyItself
		}
	}
	return keyMissed
}

// withLinks is file, and file after the links on its way when they lead
// elsewhere.
func withLinks(file string) []string {
	if r := resolved(file); r != "" {
		return []string{file, r}
	}
	return []string{file}
}

// norm is an absolute path cleaned, with / separators, each segment as
// cleanRel cleans it, in lower case: an extra prompt is harmless.
func norm(p string) string {
	return strings.ToLower(cleanRel(filepath.ToSlash(filepath.Clean(p))))
}
