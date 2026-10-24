package main

import (
	"strings"
	"testing"
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

func TestHookWarnsOfInvalidRulesWithBackstopOff(t *testing.T) {
	root := project(t, checkRules+badRule)
	writeAt(t, root, "stele.json", `{"backstop": false}`)
	if o, _ := postHook(t, root, "post-write", "a.go", "package a\n"); !strings.Contains(o.SystemMessage, "cannot be checked") {
		t.Errorf("backstop off: warning %q, want the invalid rule named", o.SystemMessage)
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

func TestHookPreToolUseNeverWarnsOfInvalidRules(t *testing.T) {
	root := project(t, checkRules+badRule)
	o, _ := hook(t, write(root+"/a.go", "p1", ""))
	if strings.Contains(o.SystemMessage, "cannot be checked") {
		t.Errorf("PreToolUse warned of invalid rules: %q", o.SystemMessage)
	}
}
