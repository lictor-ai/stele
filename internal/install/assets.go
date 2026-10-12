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
