package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPlainNumbers(t *testing.T) {
	results := []Result{
		{Scenario: "a", Arm: "claudemd", InputTokens: 100, Grade: Grade{Compliant: false, TaskOK: true, Violations: []string{"x", "y"}}},
		{Scenario: "b", Arm: "claudemd", InputTokens: 300, Grade: Grade{Compliant: true, TaskOK: true}},
		{Scenario: "a", Arm: "stele", InputTokens: 200, Denies: 1, Blocks: 2, Grade: Grade{Compliant: true, TaskOK: true}},
		// An error: counted in the runs, left out of the means.
		{Scenario: "b", Arm: "stele", InputTokens: 900, Blocks: 1, Error: "boom", Grade: Grade{Compliant: true, Violations: []string{"z"}}},
	}
	var b strings.Builder
	plain(&b, results)
	for _, want := range []string{
		"| claudemd | 2 | 0 | 1/2 | 2 | 1.00 | 0.00 | 0.00 | 200 | 2/2 |",
		"| stele | 2 | 1 | 2/2 | 1 | 0.00 | 1.00 | 2.00 | 200 | 1/2 |",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("plain numbers lack %q:\n%s", want, b.String())
		}
	}
}

func TestRanWithSkipsReusedRuns(t *testing.T) {
	got := ranWith([]Result{
		{Arm: "claudemd", AgentVersion: "2.0.0", APIKeySource: "none", ResolvedModel: "m-1"},
		{Arm: "stele", AgentVersion: "2.0.0", APIKeySource: "none", ResolvedModel: "m-1"},
		{Arm: "none", AgentVersion: "1.0.0", APIKeySource: "ANTHROPIC_API_KEY", ResolvedModel: "m-0", Baseline: "old.jsonl"},
	})
	want := []string{
		"Claude Code version: 2.0.0.",
		"Credentials (apiKeySource): none; none means no API key, for example a claude.ai login.",
		"Model each arm ran on, from Claude Code's init event: claudemd m-1; stele m-1.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ranWith = %q, want %q", got, want)
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSaveSetup(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	writeFiles(t, repo, map[string]string{
		"stele.md":                                  "rules",
		"sub/CLAUDE.md":                             "pasted rules",
		".claude/settings.json":                     "{}",
		".claude/rules/stele-rules.md":              "compiled",
		".stele/state/x":                            "state",
		".git/HEAD":                                 "ref",
		"main.go":                                   "package main",
		"services/api/stele.json":                   "{}",
		"services/api/internal/a/b.go":              "package a",
		"services/api/AGENTS.md":                    "agents",
		"services/api/stele.lock":                   "lock",
		"services/api/README.md":                    "readme",
		"services/api/.codex/hooks.json":            "{}",
		"services/api/.claude/rules/stele-rules.md": "nested compiled",
	})
	dir := filepath.Join(t.TempDir(), "run.setup")
	if err := saveSetup(repo, []byte("do it"), dir, func(b []byte) []byte { return b }); err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(got)
	want := []string{
		".claude/rules/stele-rules.md", ".claude/settings.json", "prompt.txt",
		"services/api/.claude/rules/stele-rules.md", "services/api/.codex/hooks.json",
		"services/api/AGENTS.md", "services/api/stele.json", "services/api/stele.lock",
		"stele.md", "sub/CLAUDE.md",
	}
	if !slices.Equal(got, want) {
		t.Errorf("saved %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "prompt.txt")); string(b) != "do it" {
		t.Errorf("prompt.txt = %q", b)
	}
}

func TestUngraded(t *testing.T) {
	got := ungraded([]string{
		"main.go", "CLAUDE.md", "services/api/stele.md", ".claude/settings.json",
		".claude/rules/stele-rules.md", "tool/__pycache__/a.pyc", "a/.codex/x", "docs/claude.md",
	})
	want := []string{"CLAUDE.md", "services/api/stele.md", ".claude/settings.json", "tool/__pycache__/a.pyc", "a/.codex/x"}
	if !slices.Equal(got, want) {
		t.Errorf("ungraded = %q, want %q", got, want)
	}
}

func TestOldExcusesOneFindingPerFixtureLine(t *testing.T) {
	fixture, repo := t.TempDir(), t.TempDir()
	writeFiles(t, fixture, map[string]string{"r.go": "a\n\tfmt.Printf(\"x\")\n"})
	// The agent kept the old line, copied it twice and added a blank line.
	writeFiles(t, repo, map[string]string{"r.go": "a\n\tfmt.Printf(\"x\")\n\tfmt.Printf(\"x\")\n\n\tfmt.Printf(\"x\")\n"})
	pr, other := "no fmt.Print", "another rule"
	c := &gctx{fixture: fixture, repo: repo, violations: []finding{
		{File: "r.go", Line: 2, Message: pr},
		{File: "r.go", Line: 3, Message: pr},
		{File: "r.go", Line: 5, Message: pr},
		{File: "r.go", Line: 2, Message: other}, // a second rule on the old line
		{File: "r.go", Line: 4, Message: pr},    // a blank line
	}}
	var got []bool
	for _, f := range c.violations {
		got = append(got, c.old(f))
	}
	if want := []bool{true, false, false, true, false}; !slices.Equal(got, want) {
		t.Errorf("old = %v, want %v", got, want)
	}
}
