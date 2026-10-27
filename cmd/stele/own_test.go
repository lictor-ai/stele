package main

import "testing"

// stele's own files, the rules file compile writes for the agent included,
// are never checked (#68).
func TestOwn(t *testing.T) {
	for file, want := range map[string]bool{
		"stele.md":                         true,
		"stele.json":                       true,
		"stele.lock":                       true,
		".stele/index.json":                true,
		".claude/rules/stele-rules.md":     true,
		".claude/rules/mine.md":            false,
		"sub/.claude/rules/stele-rules.md": false, // a subproject's: its own root skips it
		"internal/a.go":                    false,
	} {
		if got := own(file); got != want {
			t.Errorf("own(%q) = %v, want %v", file, got, want)
		}
	}
}
