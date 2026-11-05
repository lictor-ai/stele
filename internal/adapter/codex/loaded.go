package codex

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/safefile"
)

// Loaded says what Codex, starting a session, did with root's AGENTS.md.
//
// Codex reads, at a session's start, one file from each directory from the
// project root, the nearest one holding .git, down to where it runs:
// AGENTS.override.md if there is one, else AGENTS.md. It joins them, with a
// blank line between, and reads no more than project_doc_max_bytes of the
// whole, 32 KiB. So root's rules count as read only when all of these hold:
//
//   - the session is a startup: what a resume, compaction, clear or fork
//     keeps was not measured, so it forgets what was loaded;
//   - it runs in root or below it, and a .git is at or above root, so the
//     walk passes root, and no project_root_markers setting moves it;
//   - no .git lies between root and where it runs, which would make a
//     submodule's root the walk's top, below root;
//   - root has no AGENTS.override.md, which Codex would read instead;
//   - root's section ends within the cap, counted from the top of the walk,
//     and no HTML comment left open before it hides it;
//   - none of those files changed since the session started: Codex reads
//     them when the session starts, and this hook runs only at its first
//     turn, which may come much later (measured on 0.166.0). The start is
//     the time in the session id, a UUIDv7; an id that is not one primes
//     nothing.
//
// A subagent is never primed: its loading was not measured.
func Loaded(ev adapter.Event, root string) adapter.Load {
	st, _ := ev.Native.(start)
	switch {
	case ev.AgentID != "":
		return adapter.Load{}
	case st.Source == "startup":
	case st.Source == "":
		return adapter.Load{}
	default:
		return adapter.Load{Forget: true}
	}
	l := adapter.Load{Launch: true}
	cwd, err := filepath.Abs(ev.Cwd)
	if err != nil {
		return adapter.Load{}
	}
	cwd = filepath.Clean(cwd)
	if !within(cwd, root) {
		return l
	}
	top, ok := gitTop(root)
	if !ok {
		return l
	}
	for d := cwd; d != root; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return l // a submodule or nested checkout: Codex's walk starts there
		}
		if filepath.Dir(d) == d {
			return l
		}
	}
	started, ok := sessionStarted(ev.SessionID)
	if !ok {
		return l
	}
	limit, markers := projectDocLimit(root)
	if markers && cwd != root {
		return l
	}
	if _, err := os.Lstat(filepath.Join(root, overrideFile)); err == nil {
		return l
	}
	file := filepath.Join(root, RulesFile)
	data, err := safefile.ReadRegular(file, MaxRulesFile)
	if err != nil || changedSince(file, started) {
		return l
	}
	end := sectionEnd(data)
	if end < 0 || openComment(data[:sectionBegin(data)]) {
		return l
	}
	before := 0
	for _, d := range dirsBetween(top, root) {
		before += docSize(d)
		for _, name := range []string{overrideFile, RulesFile} {
			if changedSince(filepath.Join(d, name), started) {
				return l
			}
		}
	}
	l.Read = before+end <= limit
	return l
}

// sessionStarted is when a session began, from its id: Codex's ids are
// UUIDv7, whose first 48 bits are the Unix time in milliseconds.
func sessionStarted(id string) (time.Time, bool) {
	u, err := uuid.Parse(id)
	if err != nil || u.Version() != 7 {
		return time.Time{}, false
	}
	ms := int64(u[0])<<40 | int64(u[1])<<32 | int64(u[2])<<24 | int64(u[3])<<16 | int64(u[4])<<8 | int64(u[5])
	return time.UnixMilli(ms), true
}

// changedSince reports whether file was written after t, which a session id
// gives to the millisecond. A file that is not there did not change.
func changedSince(file string, t time.Time) bool {
	info, err := os.Stat(file)
	return err == nil && info.ModTime().After(t.Add(time.Millisecond))
}

// sectionBegin is the byte offset of the section's begin line in data, or
// len(data) when there is none.
func sectionBegin(data []byte) int {
	off := 0
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if b, _ := fence(line); b {
			return off
		}
		off += len(line)
	}
	return len(data)
}

// openComment reports whether text leaves an HTML comment open, which would
// hide what follows from a reader that hides comments.
func openComment(text []byte) bool {
	return bytes.LastIndex(text, []byte("<!--")) > bytes.LastIndex(text, []byte("-->"))
}

// within reports whether dir is root or below it.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// gitTop is the nearest directory at or above root holding .git.
func gitTop(root string) (string, bool) {
	for d := root; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// dirsBetween are the directories from top down to root's parent, in that
// order: those Codex reads before root.
func dirsBetween(top, root string) []string {
	var dirs []string
	for d := filepath.Dir(root); within(d, top) && d != root; d = filepath.Dir(d) {
		dirs = append([]string{d}, dirs...)
		if d == top || filepath.Dir(d) == d {
			break
		}
	}
	return dirs
}

// docSize is what directory d adds to what Codex reads: its override file or
// its AGENTS.md, and the blank line that joins it, nothing when it has
// neither.
func docSize(d string) int {
	for _, name := range []string{overrideFile, RulesFile} {
		if info, err := os.Stat(filepath.Join(d, name)); err == nil && info.Mode().IsRegular() {
			return int(info.Size()) + len("\n\n")
		}
	}
	return 0
}

// docLine reads a project_doc_max_bytes setting.
var docLine = regexp.MustCompile(`^\s*project_doc_max_bytes\s*=\s*(\d+)\s*(#.*)?$`)

// projectDocLimit is how much of the AGENTS files Codex reads, from the
// user's and the project's config.toml: the lowest it finds, never above
// 32 KiB. markers says a config sets project_root_markers, which moves the
// root Codex walks from. The files are scanned, not parsed as TOML: a value
// set under a profile also counts, which errs on the safe side.
func projectDocLimit(root string) (limit int, markers bool) {
	limit = maxProjectDoc
	files := []string{filepath.Join(root, filepath.FromSlash(configFile))}
	if home := codexHome(); home != "" {
		files = append(files, filepath.Join(home, "config.toml"))
	}
	for _, f := range files {
		data, err := safefile.ReadRegular(f, 1<<20)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			line := sc.Text()
			if m := docLine.FindStringSubmatch(line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n < limit {
					limit = n
				}
			}
			if strings.HasPrefix(strings.TrimSpace(line), "project_root_markers") {
				markers = true
			}
		}
	}
	return limit, markers
}

// codexHome is the directory of the user's Codex config: CODEX_HOME, or else
// ~/.codex. "" when neither can be found.
func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}
