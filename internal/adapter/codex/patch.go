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

// fileLine finds a file header anywhere, for a patch that does not parse.
var fileLine = regexp.MustCompile(`(?m)^\*\*\* (Add|Update|Delete) File: (.+?)\r?$`)

// parsePatch returns the files patch touches, paths taken relative to cwd,
// in the patch's order. A patch stele cannot follow still gives the files its
// headers name, so the guard and the rules see them; only their new text is
// lost.
func parsePatch(patch, cwd string) []adapter.Edit {
	if edits, ok := readPatch(patch, cwd); ok {
		return edits
	}
	var edits []adapter.Edit
	for _, m := range fileLine.FindAllStringSubmatch(patch, -1) {
		f, ok := patchPath(m[2], cwd)
		if !ok {
			continue
		}
		if m[1] == "Delete" {
			edits = append(edits, adapter.Edit{File: f, Removes: true})
		} else {
			edits = append(edits, adapter.Edit{File: f, Writes: true})
		}
	}
	return edits
}

// patchReader reads one patch, file by file.
type patchReader struct {
	cwd    string
	edits  []adapter.Edit
	cur    *adapter.Edit // the file being read
	adding bool          // cur is an added file: its lines are its content
	body   []string      // an added file's lines, or the new side of a hunk
	plus   bool          // the hunk being read adds a line
}

// readPatch reads a patch in Codex's format. ok is false for anything else.
func readPatch(patch, cwd string) (edits []adapter.Edit, ok bool) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) || strings.TrimSpace(lines[i]) != beginPatch {
		return nil, false
	}
	r := &patchReader{cwd: cwd}
	for _, line := range lines[i+1:] {
		if strings.TrimSpace(line) == endPatch {
			r.done()
			return r.edits, true
		}
		if !r.line(line) {
			return nil, false
		}
	}
	return nil, false // no end
}

// line reads one line of the patch; false is a line the format has no place
// for.
func (r *patchReader) line(line string) bool {
	switch {
	case strings.HasPrefix(line, addFile):
		return r.start(line[len(addFile):], true)
	case strings.HasPrefix(line, updateFile):
		return r.start(line[len(updateFile):], false)
	case strings.HasPrefix(line, deleteFile):
		r.done()
		f, ok := patchPath(line[len(deleteFile):], r.cwd)
		if ok {
			r.edits = append(r.edits, adapter.Edit{File: f, Removes: true})
		}
		return ok
	case strings.HasPrefix(line, moveTo):
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
	case line == endOfFile:
		return true
	case r.adding:
		if !strings.HasPrefix(line, "+") {
			return false
		}
		r.body = append(r.body, line[1:])
		return true
	case strings.HasPrefix(line, "@@"):
		r.hunk()
		return true
	case strings.HasPrefix(line, "+"):
		r.body, r.plus = append(r.body, line[1:]), true
		return true
	case strings.HasPrefix(line, " "):
		r.body = append(r.body, line[1:])
		return true
	case strings.HasPrefix(line, "-"):
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
	r.cur, r.adding = &adapter.Edit{File: f, Writes: true, Whole: adding}, adding
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
	r.cur, r.adding = nil, false
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
