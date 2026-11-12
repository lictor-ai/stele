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
		{Scenario: "b", Arm: "stele", InputTokens: 200, Blocks: 1, Error: "boom", Grade: Grade{Compliant: true}},
	}
	var b strings.Builder
	plain(&b, results)
	for _, want := range []string{
		"| claudemd | 2 | 1/2 | 2 | 1.00 | 0.00 | 0.00 | 200 | 2/2 | 0 |",
		"| stele | 2 | 2/2 | 0 | 0.00 | 0.50 | 1.50 | 200 | 1/2 | 1 |",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("plain numbers lack %q:\n%s", want, b.String())
		}
	}
}

func TestRanWithSkipsReusedRuns(t *testing.T) {
	got := ranWith([]Result{
		{AgentVersion: "2.0.0", APIKeySource: "none"},
		{AgentVersion: "1.0.0", APIKeySource: "ANTHROPIC_API_KEY", Baseline: "old.jsonl"},
	})
	want := []string{
		"Claude Code version: 2.0.0.",
		"Credentials (apiKeySource): none; none is a claude.ai login, billed to the subscription.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ranWith = %q, want %q", got, want)
	}
}

func TestSaveSetup(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	files := map[string]string{
		"stele.md":                       "rules",
		"sub/CLAUDE.md":                  "pasted rules",
		".claude/settings.json":          "{}",
		".claude/rules/stele-rules.md":   "compiled",
		".stele/state/x":                 "state",
		".git/HEAD":                      "ref",
		"main.go":                        "package main",
		"services/api/stele.json":        "{}",
		"services/api/internal/a/b.go":   "package a",
		"services/api/AGENTS.md":         "agents",
		"services/api/stele.lock":        "lock",
		"services/api/README.md":         "readme",
		"services/api/.codex/hooks.json": "{}",
	}
	for rel, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "run.setup")
	if err := saveSetup(repo, []byte("do it"), dir); err != nil {
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
	// A nested .codex is not the project's: only the top-level .claude and
	// .codex are setup.
	want := []string{
		".claude/rules/stele-rules.md", ".claude/settings.json", "prompt.txt",
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

func TestHomeless(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if got := homeless(filepath.Join(home, "bin", "claude")); got != filepath.Join("~", "bin", "claude") {
		t.Errorf("homeless = %q", got)
	}
}
