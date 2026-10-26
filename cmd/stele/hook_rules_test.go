package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
)

// badRule matches empty text, so the regex checker refuses it.
const badRule = "\n---\n\ncheck: regex\npattern: x*\n\nNo x.\n"

// The hook tells the user once a session about rules the post-write check
// cannot use, and still enforces the others (#54).
func TestHookWarnsOfInvalidRulesOnce(t *testing.T) {
	root := project(t, checkRules+badRule)

	o, _ := postHook(t, root, "post-write", "a.go", "package a\n")
	for _, want := range []string{"cannot be checked", "No x.", "matches empty text", "other rules still apply"} {
		if !strings.Contains(o.SystemMessage, want) {
			t.Errorf("warning %q lacks %q", o.SystemMessage, want)
		}
	}
	if strings.Contains(o.HookSpecificOutput.AdditionalContext, "cannot be checked") {
		t.Errorf("the agent was told of the invalid rule: %q", o.HookSpecificOutput.AdditionalContext)
	}

	// Once: the next write says nothing about it, and the valid rules still block.
	o, _ = postHook(t, root, "post-write", "a.go", "package a\n\nvar password = 1\n")
	if o.SystemMessage != "" {
		t.Errorf("second write warned again: %q", o.SystemMessage)
	}
	if o.Decision != "block" {
		t.Errorf("decision %q, want block: the valid rule must still be enforced", o.Decision)
	}

	// Another session is told again.
	p := postPayload(t, root, "post-write", "a.go", "package a\n")
	p["session_id"] = "s2"
	if o, _ := hook(t, p); !strings.Contains(o.SystemMessage, "cannot be checked") {
		t.Errorf("a new session was not warned: %q", o.SystemMessage)
	}
}

func TestHookWarnsAgainOfNewInvalidRules(t *testing.T) {
	root := project(t, checkRules+badRule)
	if o, _ := postHook(t, root, "post-write", "a.go", "package a\n"); o.SystemMessage == "" {
		t.Fatal("no warning for the invalid rule")
	}
	writeAt(t, root, "stele.md", checkRules+badRule+"\n---\n\ncheck: regex\n\nNo pattern at all.\n")
	o, _ := postHook(t, root, "post-write", "a.go", "package a\n")
	for _, want := range []string{"2 rules cannot be checked", "No x.", "No pattern at all."} {
		if !strings.Contains(o.SystemMessage, want) {
			t.Errorf("warning %q lacks %q", o.SystemMessage, want)
		}
	}
}

func TestHookWarnsOfInvalidRulesWithCheckOff(t *testing.T) {
	root := project(t, checkRules+badRule)
	if o, _ := hook(t, postPayload(t, root, "post-write", "a.go", "package a\n"), "--check=off"); !strings.Contains(o.SystemMessage, "cannot be checked") {
		t.Errorf("check off: warning %q, want the invalid rule named", o.SystemMessage)
	}
}

// When the agent writes stele.md itself, it is told too, every time, so it
// knows whether its rule works.
func TestHookTellsAgentOfInvalidRuleInSteleMD(t *testing.T) {
	root := project(t, checkRules)
	for i := range 2 {
		o, _ := postHook(t, root, "post-write", "stele.md", checkRules+badRule)
		if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "matches empty text") {
			t.Errorf("write %d of stele.md: context %q, want the problem", i+1, o.HookSpecificOutput.AdditionalContext)
		}
		if got := strings.Contains(o.SystemMessage, "cannot be checked"); got != (i == 0) {
			t.Errorf("write %d of stele.md: user warning %q, want it only the first time", i+1, o.SystemMessage)
		}
	}
}

// longBadRules is n invalid rules whose 2,000-character patterns match empty
// text, so each problem message quotes a long pattern.
func longBadRules(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "\n---\n\ncheck: regex\npattern: %s\n\nLong rule %d.\n", strings.Repeat("y?", 1000), i)
	}
	return b.String()
}

// Claude Code caps a hook's text at 10,000 characters: the warning, and the
// agent's context on a stele.md write, stay under install.MaxMessage.
func TestHookInvalidRulesMessageIsCapped(t *testing.T) {
	root := project(t, checkRules+longBadRules(6))
	o, _ := postHook(t, root, "post-write", "a.go", "package a\n")
	if n := len(o.SystemMessage); n == 0 || n > install.MaxMessage {
		t.Fatalf("warning is %d bytes, want 1..%d", n, install.MaxMessage)
	}
	for _, want := range []string{"6 rules cannot be checked", "…", "and 1 more"} {
		if !strings.Contains(o.SystemMessage, want) {
			t.Errorf("warning lacks %q:\n%s", want, o.SystemMessage)
		}
	}

	o, _ = postHook(t, root, "post-write", "stele.md", checkRules+longBadRules(6))
	if n := len(o.HookSpecificOutput.AdditionalContext); n == 0 || n > install.MaxMessage {
		t.Errorf("context on a stele.md write is %d bytes, want 1..%d", n, install.MaxMessage)
	}
}

func TestInvalidRulesMessageWithin(t *testing.T) {
	ps := make([]check.Problem, 4)
	for i := range ps {
		ps[i] = check.Problem{RuleID: fmt.Sprintf("r%d", i), Excerpt: "A rule.", Message: strings.Repeat("m", 500)}
	}
	full := invalidRulesMessage(index.Index{}, ps, install.MaxMessage)
	if strings.Contains(full, strings.Repeat("m", 201)) || !strings.Contains(full, strings.Repeat("m", 200)+"…") {
		t.Errorf("a problem message is not cut at 200 characters:\n%s", full)
	}
	for _, limit := range []int{900, 500, 200} {
		got := invalidRulesMessage(index.Index{}, ps, limit)
		if len(got) > limit {
			t.Errorf("limit %d: %d bytes:\n%s", limit, len(got), got)
		}
		if got != "" && !strings.Contains(got, "more") {
			t.Errorf("limit %d: entries left out but not counted:\n%s", limit, got)
		}
	}
	if got := invalidRulesMessage(index.Index{}, ps, 20); got != "" {
		t.Errorf("limit 20: %q, want nothing when not even the summary fits", got)
	}
}

func TestHookPreToolUseNeverWarnsOfInvalidRules(t *testing.T) {
	root := project(t, checkRules+badRule)
	o, _ := hook(t, write(root+"/a.go", "p1", ""))
	if strings.Contains(o.SystemMessage, "cannot be checked") {
		t.Errorf("PreToolUse warned of invalid rules: %q", o.SystemMessage)
	}
}
