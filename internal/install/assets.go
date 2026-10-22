package install

import (
	_ "embed"
	"strings"
	"text/template"
	"unicode/utf8"
)

// blockMessage is what the agent reads when the hook pauses a write to show
// it the rules. It is a file, not a string in the code, because it is one of
// the two texts that decide whether stele works at all: tune it there.
//
//go:embed assets/block-message.tmpl
var blockMessage string

var blockTemplate = template.Must(template.New("block-message").Parse(blockMessage))

// BlockRule is one rule in a block message.
type BlockRule struct {
	ID, Text string
}

// BlockMessage renders the message for a write to path that needs rules. note,
// when set, says something the agent should know, such as stele.md failing to
// parse.
func BlockMessage(path string, rules []BlockRule, note string) string {
	var b strings.Builder
	err := blockTemplate.Execute(&b, struct {
		Path  string
		Rules []BlockRule
		Note  string
	}{path, rules, note})
	if err != nil {
		panic(err) // the template is compiled in and tested
	}
	return strings.TrimSpace(b.String())
}

// violationMessage is what the agent reads when a write it just made breaks
// a rule stele can check. It is the other text that decides whether stele
// works: tune it there.
//
//go:embed assets/violation-message.tmpl
var violationMessage string

var violationTemplate = template.Must(template.New("violation-message").Parse(violationMessage))

// Violation is one place in a violation message.
type Violation struct {
	Line, Column             int
	RuleID, Excerpt, Message string
}

// MaxMessage bounds a violation message, in bytes. Claude Code passes a hook
// text on in full only up to 10,000 characters; past that Claude sees a
// preview.
const MaxMessage = 9000

// maxRuleMessage bounds one rule's message, in characters.
const maxRuleMessage = 200

// ViolationMessage renders the message for violations found in a write to
// path. more counts those left out. blocking picks the wording: fix these
// now, or fix them if they are part of the change. A rule's message is cut
// to maxRuleMessage characters, and violations are left out, counted in
// more, until the whole fits in MaxMessage.
func ViolationMessage(path string, vs []Violation, more int, blocking bool) string {
	return ViolationMessageWithin(path, vs, more, blocking, MaxMessage)
}

// ViolationMessageWithin is ViolationMessage fitting in limit bytes, for a
// message that shares its text with others. With no violation left it may
// still be longer.
func ViolationMessageWithin(path string, vs []Violation, more int, blocking bool, limit int) string {
	cut := make([]Violation, len(vs))
	for i, v := range vs {
		v.Message = truncate(v.Message, maxRuleMessage)
		cut[i] = v
	}
	for n := len(cut); ; n-- {
		s := renderViolations(path, cut[:n], more+len(cut)-n, blocking)
		if len(s) <= limit || n == 0 {
			return s
		}
	}
}

func renderViolations(path string, vs []Violation, more int, blocking bool) string {
	var b strings.Builder
	err := violationTemplate.Execute(&b, struct {
		Path       string
		Violations []Violation
		More       int
		Blocking   bool
	}{path, vs, more, blocking})
	if err != nil {
		panic(err) // the template is compiled in and tested
	}
	return strings.TrimSpace(b.String())
}

// truncate cuts s to n characters, marking the cut with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
