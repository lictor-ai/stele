package codex

import (
	"bytes"
	_ "embed"
	"regexp"
	"strings"
	"text/template"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/rulesblock"
)

// RulesFile is where Codex finds the rules (ADR-0012): the AGENTS.md it loads
// at the start of every session, from the project root down to where it
// runs. The file is the user's; stele writes only its own section of it.
const RulesFile = "AGENTS.md"

// overrideFile replaces AGENTS.md in a directory where it exists: Codex then
// reads it instead.
const overrideFile = "AGENTS.override.md"

// MaxRulesFile caps a read of RulesFile.
const MaxRulesFile = 16 << 20

// maxProjectDoc is how much of the AGENTS files, joined, Codex reads: its
// project_doc_max_bytes, 32 KiB unless the user's config lowers it.
const maxProjectDoc = 32 << 10

// MaxRulesChars is the size past which the file is worth a warning: Codex
// reads only the first 32 KiB of its AGENTS files together.
const MaxRulesChars = 24 << 10

// The lines that fence stele's section. The begin line is matched by its
// start, so its words may change between versions.
const (
	beginPrefix = "<!-- stele:begin"
	endLine     = "<!-- stele:end -->"
)

// rulesSection is the section's text.
//
//go:embed assets/rules-section.tmpl
var rulesSection string

var sectionTemplate = template.Must(template.New("rules-section").Funcs(template.FuncMap{
	"join": func(s []string) string { return strings.Join(s, ", ") },
}).Parse(rulesSection))

// fence reports whether line, its ends trimmed, is one of the section's
// marker lines.
func fence(line string) (begin, end bool) {
	l := strings.TrimSpace(line)
	return beginLine.MatchString(l), l == endLine
}

// beginLine is the section's begin line: the marker, and maybe words after a
// colon, which a stele of another version may word differently.
var beginLine = regexp.MustCompile(`^<!-- stele:begin(?:: [^\n]*?)? -->$`)

// fenced reports whether a rule's text holds a line that would read as a
// marker of the section.
func fenced(r adapter.Rule) bool {
	for _, s := range append([]string{r.Body}, r.Scope...) {
		for _, line := range strings.Split(s, "\n") {
			if b, e := fence(line); b || e {
				return true
			}
		}
	}
	return false
}

// RenderRules returns stele's section for rules, in their order, markers
// included. A rule whose id cannot be a marker, or whose text holds a line
// that would end the section, is left out, and stays covered by the hook's
// deny.
func RenderRules(rules []adapter.Rule) []byte {
	var keep []adapter.Rule
	for _, r := range rules {
		if rulesblock.ValidID(r.ID) && !fenced(r) {
			keep = append(keep, r)
		}
	}
	var b bytes.Buffer
	if err := sectionTemplate.Execute(&b, keep); err != nil {
		panic(err) // the template is compiled in and tested
	}
	return append(bytes.TrimRight(b.Bytes(), "\n"), '\n')
}

// RuleHash is the hash ReadRules gives r's block in a section RenderRules
// wrote, or "" when r is never taken as read: a rule left out of the
// section, one whose block would not read back as r alone, or one holding an
// HTML comment's opening or closing, which could hide the rest of the rule
// as the agent reads it.
func RuleHash(r adapter.Rule) string {
	if fenced(r) {
		return ""
	}
	for _, s := range append([]string{r.Body}, r.Scope...) {
		if strings.Contains(s, "<!--") || strings.Contains(s, "-->") {
			return ""
		}
	}
	var b bytes.Buffer
	if err := sectionTemplate.ExecuteTemplate(&b, "block", r); err != nil {
		panic(err)
	}
	got := rulesblock.Read(b.Bytes())
	if len(got) != 1 {
		return ""
	}
	return got[r.ID]
}

// section finds stele's section in the lines of a file: the index of its
// begin and end lines. found is false when there is none; ok is false when
// the markers are broken: more than one of either, or out of order.
func section(lines []string) (begin, end int, found, ok bool) {
	begin, end = -1, -1
	for i, l := range lines {
		b, e := fence(l)
		switch {
		case b && begin >= 0, e && end >= 0:
			return 0, 0, false, false
		case b:
			begin = i
		case e:
			end = i
		}
	}
	switch {
	case begin < 0 && end < 0:
		return 0, 0, false, true
	case begin < 0 || end < begin:
		return 0, 0, false, false
	}
	return begin, end, true, true
}

// text is a file split into lines, each with its own line end, so every
// byte outside stele's section is written back as it was.
type text struct {
	bom   string
	lines []string // each with its line end; the last may have none
}

func splitText(data []byte) text {
	body, bom := bytes.CutPrefix(data, []byte("\xef\xbb\xbf"))
	t := text{lines: strings.SplitAfter(string(body), "\n")}
	if bom {
		t.bom = "\xef\xbb\xbf"
	}
	if n := len(t.lines); n > 0 && t.lines[n-1] == "" {
		t.lines = t.lines[:n-1]
	}
	return t
}

func (t text) join(lines []string) []byte { return []byte(t.bom + strings.Join(lines, "")) }

// ending is a line's line end: "\r\n", "\n", or "" for a last line with none.
func ending(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

// lastEnding is the line end of the file's last line that has one, "\n"
// when none has.
func (t text) lastEnding() string {
	for i := len(t.lines) - 1; i >= 0; i-- {
		if e := ending(t.lines[i]); e != "" {
			return e
		}
	}
	return "\n"
}

// renderedLines are the section's lines, each ended with nl.
func renderedLines(rendered []byte, nl string) []string {
	plain := strings.TrimRight(strings.ReplaceAll(string(rendered), "\r\n", "\n"), "\n")
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] += nl
	}
	return lines
}

// utf16 reports whether data looks like UTF-16 text, which stele does not
// write into: its byte order mark, or a NUL byte.
func utf16(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) || bytes.IndexByte(data, 0) >= 0
}

// SpliceRules returns disk with rendered, a section RenderRules wrote, in
// place of stele's section, its lines ended as the begin line was. A file
// with no section gets it appended after a blank line, in the line end of
// the file's last line. Every byte outside the section is kept; a last line
// with no line end gets one. ok is false when disk's markers are broken, or
// it is UTF-16: the file is then not stele's to change. disk is nil when
// there is no file.
func SpliceRules(disk, rendered []byte) (out []byte, ok bool) {
	if utf16(disk) {
		return nil, false
	}
	t := splitText(disk)
	begin, end, found, ok := section(t.lines)
	if !ok {
		return nil, false
	}
	if found {
		nl := ending(t.lines[begin])
		if nl == "" {
			nl = t.lastEnding()
		}
		sec := renderedLines(rendered, nl)
		if ending(t.lines[end]) == "" {
			sec[len(sec)-1] = strings.TrimSuffix(sec[len(sec)-1], nl)
		}
		lines := append(append(append([]string{}, t.lines[:begin]...), sec...), t.lines[end+1:]...)
		return t.join(lines), true
	}
	nl := t.lastEnding()
	lines := append([]string{}, t.lines...)
	if n := len(lines); n > 0 {
		if ending(lines[n-1]) == "" {
			lines[n-1] += nl
		}
		lines = append(lines, nl) // a blank line before the section
	}
	return t.join(append(lines, renderedLines(rendered, nl)...)), true
}

// UnspliceRules returns disk without stele's section, and the one blank line
// before it; remove says nothing else is left. ok is false when disk holds
// no section, a broken one, or is UTF-16.
func UnspliceRules(disk []byte) (out []byte, remove, ok bool) {
	if utf16(disk) {
		return nil, false, false
	}
	t := splitText(disk)
	begin, end, found, ok := section(t.lines)
	if !ok || !found {
		return nil, false, false
	}
	from := begin
	if from > 0 && strings.TrimSpace(t.lines[from-1]) == "" {
		from--
	}
	lines := append(append([]string{}, t.lines[:from]...), t.lines[end+1:]...)
	out = t.join(lines)
	return out, len(strings.TrimSpace(strings.TrimPrefix(string(out), "\xef\xbb\xbf"))) == 0, true
}

// sectionText is the text inside stele's section, markers left out, or nil
// when there is none to read.
func sectionText(data []byte) []byte {
	t := splitText(data)
	begin, end, found, ok := section(t.lines)
	if !found || !ok {
		return nil
	}
	return []byte(strings.Join(t.lines[begin+1:end], ""))
}

// OwnedRules reports whether data holds stele's section, well fenced.
func OwnedRules(data []byte) bool {
	t := splitText(data)
	_, _, found, ok := section(t.lines)
	return found && ok
}

// ReadRules returns the rules in the section as the agent read it: rule id to
// the hash of its block. Only the section counts: the user's text around it
// is no rule's.
func ReadRules(data []byte) map[string]string { return rulesblock.Read(sectionText(data)) }

// SameRules reports whether the file on disk gives the agent the same rules,
// in the same order, as want: only the sections' blocks count, so neither
// the user's text nor the section's intro makes a rewrite.
func SameRules(disk, want []byte) bool {
	return OwnedRules(disk) && OwnedRules(want) && rulesblock.Same(sectionText(disk), sectionText(want))
}

// sectionEnd is the byte offset, in data, just past the section's end line,
// or -1 when there is no section.
func sectionEnd(data []byte) int {
	off := 0
	for _, line := range strings.SplitAfter(string(data), "\n") {
		off += len(line)
		if _, e := fence(line); e {
			return off
		}
	}
	return -1
}
