package main

import (
	"os"
	"path/filepath"
	"testing"
)

// stele's own files, the rules file compile writes for the agent included,
// are never checked (#68).
func TestOwn(t *testing.T) {
	root := t.TempDir()
	for file, want := range map[string]bool{
		"stele.md":                         true,
		"stele.json":                       true,
		"stele.lock":                       true,
		".stele/index.json":                true,
		".claude/rules/stele-rules.md":     true,
		".claude/rules/mine.md":            false,
		"sub/.claude/rules/stele-rules.md": false, // a subproject's: its own root skips it
		"internal/a.go":                    false,
		"AGENTS.md":                        false, // the user's, with no stele section
	} {
		if got := own(root, file); got != want {
			t.Errorf("own(%q) = %v, want %v", file, got, want)
		}
	}
}

// A user's AGENTS.md is checked like any file until it holds stele's
// section, which holds every rule's text (ADR-0012).
func TestOwnSharedRulesFile(t *testing.T) {
	root := t.TempDir()
	codex, ok := agentNamed("codex")
	if !ok {
		t.Fatal("no codex agent")
	}
	file := filepath.Join(root, filepath.FromSlash(codex.RulesFile()))
	if err := os.WriteFile(file, []byte("# Mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if own(root, codex.RulesFile()) {
		t.Error("an AGENTS.md without stele's section is skipped")
	}
	out, _ := codex.SpliceRules([]byte("# Mine\n"), codex.RenderRules(nil))
	if err := os.WriteFile(file, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if !own(root, codex.RulesFile()) {
		t.Error("an AGENTS.md holding stele's section is checked")
	}
}
