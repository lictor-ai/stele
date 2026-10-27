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
	return BlockMessageMore(path, rules, 0, note)
}

// BlockMessageMore is BlockMessage with more rules left out, counted.
func BlockMessageMore(path string, rules []BlockRule, more int, note string) string {
	var b strings.Builder
	err := blockTemplate.Execute(&b, struct {
		Path  string
		Rules []BlockRule
		More  int
		Note  string
	}{path, rules, more, note})
	if err != nil {
		panic(err) // the template is compiled in and tested
	}
	return strings.TrimSpace(b.String())
}

// bashMessage is what the agent reads when the hook stops a Bash command
// that writes files with rules (#57): use Write or Edit, which stele sees.
//
//go:embed assets/bash-message.tmpl
var bashMessage string

var bashTemplate = template.Must(template.New("bash-message").Parse(bashMessage))

// BashMessage renders the message for a Bash command that writes paths.
// rules are those the agent has not seen; note is as for BlockMessage.
func BashMessage(paths []string, rules []BlockRule, note string) string {
	return BashMessageMore(paths, rules, 0, note)
}

// BashMessageMore is BashMessage with more rules left out, counted.
func BashMessageMore(paths []string, rules []BlockRule, more int, note string) string {
	var b strings.Builder
	err := bashTemplate.Execute(&b, struct {
		Paths string
		Rules []BlockRule
		More  int
		Note  string
	}{strings.Join(paths, ", "), rules, more, note})
	if err != nil {
		panic(err) // the template is compiled in and tested
	}
	return strings.TrimSpace(b.String())
}

// askMessage is what the user reads in Claude Code's permission prompt when
// the agent writes one of stele's own files.
//
//go:embed assets/ask-message.tmpl
var askMessage string

var askTemplate = template.Must(template.New("ask-message").Parse(askMessage))

// AskMessage renders the prompt for a write to path, one of stele's own files.
func AskMessage(path string) string {
	var b strings.Builder
	if err := askTemplate.Execute(&b, struct{ Path string }{path}); err != nil {
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

// FitRules returns how many of n rules, kept whole and in order, fit in a
// message within limit bytes; render renders it with the first k. Rules are
// not cut: Claude Code passes a hook text on in full only up to 10,000
// characters, and a rule cut off in a preview would count as shown. It is 0
// when not even one fits.
func FitRules(n, limit int, render func(k int) string) int {
	lo, hi := 0, n // the answer is in [lo, hi]
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(render(mid)) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// truncate cuts s to n characters, marking the cut with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
