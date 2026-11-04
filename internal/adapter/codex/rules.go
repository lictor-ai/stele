package codex

import (
	"bytes"
	_ "embed"
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
	return strings.HasPrefix(l, beginPrefix), l == endLine
}

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

// text is a file split into lines, with how it was written.
type text struct {
	bom   bool
	crlf  bool
	lines []string // without their line ends; the last is "" when the file ends in one
}

func splitText(data []byte) text {
	body, bom := bytes.CutPrefix(data, []byte("\xef\xbb\xbf"))
	s := string(body)
	t := text{bom: bom, crlf: strings.Contains(s, "\r\n")}
	t.lines = strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	return t
}

func (t text) join(lines []string) []byte {
	nl := "\n"
	if t.crlf {
		nl = "\r\n"
	}
	s := strings.Join(lines, nl)
	if t.bom {
		s = "\xef\xbb\xbf" + s
	}
	return []byte(s)
}

// renderedLines are the section's lines, without a final empty one.
func renderedLines(rendered []byte) []string {
	return strings.Split(strings.TrimRight(strings.ReplaceAll(string(rendered), "\r\n", "\n"), "\n"), "\n")
}

// SpliceRules returns disk with rendered, a section RenderRules wrote, in
// place of stele's section, in disk's line ends and byte order mark. A file
// with no section gets it appended after a blank line. Every byte outside
// the section is kept. ok is false when disk's markers are broken: the file
// is then not stele's to change.
func SpliceRules(disk, rendered []byte) (out []byte, ok bool) {
	t := splitText(disk)
	begin, end, found, ok := section(t.lines)
	if !ok {
		return nil, false
	}
	sec := renderedLines(rendered)
	if found {
		lines := append(append(append([]string{}, t.lines[:begin]...), sec...), t.lines[end+1:]...)
		return t.join(lines), true
	}
	lines := t.lines
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // the final line end
	}
	if len(lines) > 0 {
		lines = append(lines, "") // a blank line before the section
	}
	lines = append(append(lines, sec...), "")
	return t.join(lines), true
}

// UnspliceRules returns disk without stele's section, and the one blank line
// before it; remove says nothing else is left. ok is false when disk holds
// no section, or a broken one.
func UnspliceRules(disk []byte) (out []byte, remove, ok bool) {
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
	return []byte(strings.Join(t.lines[begin+1:end], "\n"))
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
