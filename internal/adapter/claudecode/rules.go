package claudecode

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"
	"text/template"
)

// RulesFile is where stele writes the project's rules for Claude Code to load
// (#68), relative to a root, in slash form. With no paths: frontmatter,
// Claude Code loads a file in .claude/rules/ at launch, as it loads CLAUDE.md:
// whole, with none of the 10,000-character cap on a hook's text, again after
// a resume or a compaction, and in each subagent. It is not named stele.md:
// a directory holding a stele.md is a stele root.
const RulesFile = ".claude/rules/stele-rules.md"

// MaxRulesFile caps a read of RulesFile. stele.md is capped at 4 MiB; its
// rules rendered with their scopes stay well under this.
const MaxRulesFile = 16 << 20

// Rule is one rule as the file shows it.
type Rule struct {
	ID    string
	Scope []string
	Body  string
}

// rulesFile is the file's text. It is a file, not a string in the code: it is
// what the agent reads before its first prompt.
//
//go:embed assets/rules-file.tmpl
var rulesFile string

var rulesTemplate = template.Must(template.New("rules-file").Funcs(template.FuncMap{
	"join": func(s []string) string { return strings.Join(s, ", ") },
}).Parse(rulesFile))

// marker opens a rule's block. The id's characters are those of a lock id; a
// rule whose id has others is left out of the file.
var marker = regexp.MustCompile(`^<!-- stele rule ([A-Za-z0-9._:-]{1,128}) -->$`)

// RenderRules returns the file for rules, in their order. A rule whose id
// cannot be a marker is left out, and stays covered by the hook's deny.
func RenderRules(rules []Rule) []byte {
	var keep []Rule
	for _, r := range rules {
		if marker.MatchString("<!-- stele rule " + r.ID + " -->") {
			keep = append(keep, r)
		}
	}
	var b bytes.Buffer
	if err := rulesTemplate.Execute(&b, keep); err != nil {
		panic(err) // the template is compiled in and tested
	}
	return append(bytes.TrimRight(b.Bytes(), "\n"), '\n')
}

// RuleHash is the hash ReadRules gives r's block in a file RenderRules wrote.
// It is "" when the block would not read back as r alone, such as a body
// holding a marker line: such a rule is never taken as read.
func RuleHash(r Rule) string {
	var b bytes.Buffer
	if err := rulesTemplate.ExecuteTemplate(&b, "block", r); err != nil {
		panic(err)
	}
	got := ReadRules(b.Bytes())
	if len(got) != 1 {
		return ""
	}
	return got[r.ID]
}

// ReadRules returns the rules in a file as the agent read it: rule id to a
// hash of its block's text. It is the only code that hashes a block, so the
// file's side and RuleHash agree by construction. Line ends, a byte order
// mark, trailing spaces and blank lines between blocks do not count, as a
// checkout may change them; any other change to a block's text, its scope
// line included, does. An id found twice is dropped, every copy.
func ReadRules(data []byte) map[string]string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	got := map[string]string{}
	seen := map[string]bool{}
	id, start := "", -1
	flush := func(end int) {
		if id == "" {
			return
		}
		if seen[id] {
			delete(got, id)
			return
		}
		seen[id] = true
		got[id] = blockHash(id, lines[start:end])
	}
	for i, line := range lines {
		m := marker.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
		if m == nil {
			continue
		}
		flush(i)
		id, start = m[1], i+1
	}
	flush(len(lines))
	return got
}

// blockHash hashes a block's lines, trimmed at the right, without the blank
// lines around them. The version prefix lets a later format prime nothing,
// which is safe, rather than prime the wrong text.
func blockHash(id string, lines []string) string {
	trimmed := make([]string, len(lines))
	for i, l := range lines {
		trimmed[i] = strings.TrimRight(l, " \t\r")
	}
	for len(trimmed) > 0 && trimmed[0] == "" {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	sum := sha256.Sum256([]byte("stele-rule-v1\n" + id + "\n" + strings.Join(trimmed, "\n")))
	return hex.EncodeToString(sum[:16])
}
