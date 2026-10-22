package install

import (
	_ "embed"
	"strings"
	"text/template"
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

// ViolationMessage renders the message for violations found in a write to
// path. more counts those left out. blocking picks the wording: fix these
// now, or fix them if they are part of the change.
func ViolationMessage(path string, vs []Violation, more int, blocking bool) string {
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
