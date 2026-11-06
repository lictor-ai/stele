package codex

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
)

// The lines of Codex's patch format, which apply_patch takes: one patch,
// any number of files, each added, updated (and maybe moved) or deleted.
//
//	*** Begin Patch
//	*** Add File: path
//	+line
//	*** Update File: path
//	*** Move to: new path
//	@@ optional context
//	 context
//	-old
//	+new
//	*** End of File
//	*** Delete File: path
//	*** End Patch
const (
	beginPatch = "*** Begin Patch"
	endPatch   = "*** End Patch"
	addFile    = "*** Add File: "
	updateFile = "*** Update File: "
	deleteFile = "*** Delete File: "
	moveTo     = "*** Move to: "
	endOfFile  = "*** End of File"
)

// fileLine finds a file header anywhere, even indented or inside a line, for
// a patch that does not parse: whatever Codex might take as a header is a
// file the guard sees.
var fileLine = regexp.MustCompile(`\*\*\* (Add File|Update File|Delete File|Move to): ([^\r\n]+)`)

// heredoc opens a patch wrapped as the shell takes it, which Codex also
// accepts: <<'EOF', <<"EOF" or <<EOF, maybe after apply_patch.
var heredoc = regexp.MustCompile(`^\s*(?:apply_?patch\s+)?<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?[ \t]*\r?$`)

// headerLine finds a line that is a file header, maybe indented: what a
// lenient reader could take as one.
var headerLine = regexp.MustCompile(`(?m)^[ \t]*\*\*\* (Add File|Update File|Delete File|Move to): ([^\r\n]+)`)

// parsePatch returns the files patch touches, paths taken relative to cwd,
// in the patch's order. Every file a header-looking line names is among
// them, whatever stele read the patch as: a reader that disagreed with
// Codex on where a file or the patch ends must not hide one from the guard.
// A patch stele cannot follow gives every file a header anywhere in it
// names; only their new text is lost.
func parsePatch(patch, cwd string) []adapter.Edit {
	whole := patch // the union reads it all: a wrapper's end may be a guess
	edits, ok := readPatch(unwrap(patch), cwd)
	re := headerLine
	if !ok {
		edits, re = nil, fileLine
	}
	seen := map[string]bool{}
	for _, e := range edits {
		seen[e.File] = true
	}
	for _, m := range re.FindAllStringSubmatch(whole, -1) {
		f, ok := patchPath(m[2], cwd)
		if !ok || seen[f] {
			continue
		}
		seen[f] = true
		if m[1] == "Delete File" {
			edits = append(edits, adapter.Edit{File: f, Removes: true})
		} else {
			edits = append(edits, adapter.Edit{File: f, Writes: true})
		}
	}
	return edits
}

// unwrap takes a heredoc's wrapper off a patch: its first line and its
// closing tag.
func unwrap(patch string) string {
	first, rest, ok := strings.Cut(patch, "\n")
	m := heredoc.FindStringSubmatch(first)
	if !ok || m == nil {
		return patch
	}
	lines := strings.Split(rest, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == m[2] {
			return strings.Join(lines[:i], "\n")
		}
	}
	return rest
}

// patchReader reads one patch, file by file.
type patchReader struct {
	cwd    string
	edits  []adapter.Edit
	cur    *adapter.Edit // the file being read
	adding bool          // cur is an added file: its lines are its content
	body   []string      // an added file's lines, or the new side of a hunk
	plus   bool          // the hunk being read adds a line
	inHunk bool          // an update's hunk is being read: its lines are content
}

// readPatch reads a patch in Codex's format. As Codex does, the patch is
// what lies between its first and its last line that are not blank, which
// must be the begin and end markers. ok is false for anything else.
func readPatch(patch, cwd string) (edits []adapter.Edit, ok bool) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	first, last := 0, len(lines)-1
	for first <= last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last >= first && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if first >= last || strings.TrimSpace(lines[first]) != beginPatch || strings.TrimSpace(lines[last]) != endPatch {
		return nil, false
	}
	r := &patchReader{cwd: cwd}
	for _, line := range lines[first+1 : last] {
		if !r.line(line) {
			return nil, false
		}
	}
	r.done()
	return r.edits, true
}

// line reads one line of the patch; false is a line the format has no place
// for.
func (r *patchReader) line(line string) bool {
	// Codex trims a header line, so an indented one is a header too; but in
	// a file's body a line opening with a space, + or - is content, whatever
	// follows.
	h := strings.TrimSpace(line)
	body := r.adding || r.inHunk
	content := r.cur != nil && body && line != "" && strings.ContainsRune(" +-", rune(line[0]))
	if content || !strings.HasPrefix(h, "*** ") {
		h = ""
	}
	if h == endPatch || h == beginPatch {
		return false // a marker inside the patch: not stele's to guess
	}
	switch {
	case strings.HasPrefix(h, addFile):
		return r.start(h[len(addFile):], true)
	case strings.HasPrefix(h, updateFile):
		return r.start(h[len(updateFile):], false)
	case strings.HasPrefix(h, deleteFile):
		r.done()
		f, ok := patchPath(h[len(deleteFile):], r.cwd)
		if ok {
			r.edits = append(r.edits, adapter.Edit{File: f, Removes: true})
		}
		return ok
	case strings.HasPrefix(h, moveTo):
		line = h
		// An update's new path: the old one goes, and the file is written
		// there in full.
		if r.cur == nil || r.adding {
			return false
		}
		f, ok := patchPath(line[len(moveTo):], r.cwd)
		if !ok {
			return false
		}
		r.edits = append(r.edits, adapter.Edit{File: r.cur.File, Removes: true})
		r.cur.File, r.cur.Whole = f, true
		return true
	case r.cur == nil:
		return strings.TrimSpace(line) == ""
	case h == endOfFile:
		return true
	case r.adding:
		if !strings.HasPrefix(line, "+") {
			return false
		}
		r.body = append(r.body, line[1:])
		return true
	case !content && strings.HasPrefix(strings.TrimLeft(line, " \t"), "@@"):
		r.hunk()
		r.inHunk = true
		return true
	case strings.HasPrefix(line, "+"):
		r.body, r.plus, r.inHunk = append(r.body, line[1:]), true, true
		return true
	case strings.HasPrefix(line, " "):
		r.body, r.inHunk = append(r.body, line[1:]), true
		return true
	case strings.HasPrefix(line, "-"):
		r.inHunk = true
		return true
	case line == "":
		r.body = append(r.body, "") // a blank context line, its space trimmed
		return true
	}
	return false
}

// start begins a file at path p.
func (r *patchReader) start(p string, adding bool) bool {
	r.done()
	f, ok := patchPath(p, r.cwd)
	if !ok {
		return false
	}
	r.cur, r.adding, r.inHunk = &adapter.Edit{File: f, Writes: true, Whole: adding}, adding, false
	return true
}

// hunk ends the hunk being read: its new side is what check finds in the
// file, when it adds a line. One that only removes lines writes none.
func (r *patchReader) hunk() {
	if r.cur != nil && !r.adding && r.plus {
		r.cur.NewText = append(r.cur.NewText, strings.Join(r.body, "\n"))
		r.cur.ReplaceAll = append(r.cur.ReplaceAll, false)
	}
	r.body, r.plus = nil, false
}

// done ends the file being read.
func (r *patchReader) done() {
	if r.cur == nil {
		return
	}
	if r.adding {
		r.cur.NewText = []string{strings.Join(r.body, "\n")}
		r.body = nil
	} else {
		r.hunk()
	}
	r.edits = append(r.edits, *r.cur)
	r.cur, r.adding, r.inHunk = nil, false, false
}

// patchPath is a path in a patch, relative to cwd unless absolute, cleaned.
func patchPath(p, cwd string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return "", false
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), true
}
