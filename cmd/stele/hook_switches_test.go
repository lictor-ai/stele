package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/index"
)

var (
	remindOff = []string{"--installed=v1", "--remind=off"}
	checkOff  = []string{"--installed=v1", "--check=off"}
)

// With --remind=off no write is denied to show its rules (ADR-0006), even
// when stele cannot run and block_on_error is on: that part does not run.
func TestHookRemindOff(t *testing.T) {
	clock(t, time.Now())
	root := project(t, hookRules)
	if _, out := hook(t, write(filepath.Join(root, "a.go"), "p1", ""), remindOff...); out != "" {
		t.Errorf("remind off: stdout %q, want nothing", out)
	}

	writeAt(t, root, "stele.json", `{"block_on_error": true}`)
	old := keyDir
	keyDir = func() (string, error) { return "", errors.New("no config directory") }
	t.Cleanup(func() { keyDir = old })
	if _, out := hook(t, write(filepath.Join(root, "b.go"), "p1", ""), remindOff...); out != "" {
		t.Errorf("remind off, no key, block_on_error: stdout %q, want nothing", out)
	}
}

// Session hooks run as they always do with --remind=off: the guidance, and
// the rules the session loaded, which spare a deny if remind is turned back
// on.
func TestHookSessionStartRemindOff(t *testing.T) {
	root := primedProject(t)
	p := sessionPayload(t, "SessionStart", root)
	p["session_id"], p["source"] = "s1", "startup"
	on, _ := hook(t, p)
	off, _ := hook(t, p, remindOff...)
	if off.HookSpecificOutput.AdditionalContext == "" || off.HookSpecificOutput.AdditionalContext != on.HookSpecificOutput.AdditionalContext {
		t.Errorf("SessionStart with remind off: context %q, want the same as with it on, %q",
			off.HookSpecificOutput.AdditionalContext, on.HookSpecificOutput.AdditionalContext)
	}
	if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "")); denied(o) {
		t.Errorf("a write needing only loaded rules was denied: the session was not primed: %s", o.HookSpecificOutput.PermissionDecisionReason)
	}
}

// With --check=off no write is checked, but the hook still recompiles, so
// the index and the rules file follow stele.md, and invalid rules are still
// reported.
func TestHookCheckOff(t *testing.T) {
	t.Run("no check", func(t *testing.T) {
		root := project(t, checkRules)
		o, out := hook(t, postPayload(t, root, "post-write", "a.go", "package a\n\nvar password = 1\n"), checkOff...)
		if o.Decision != "" || strings.Contains(o.HookSpecificOutput.AdditionalContext, "a.go:3") {
			t.Errorf("check off: %s, want no block and no violation", out)
		}
	})
	t.Run("stele.md written: index and rules file refreshed", func(t *testing.T) {
		root := project(t, checkRules)
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile = %d: %s", code, errOut)
		}
		added := checkRules + "\n---\n\nName every goroutine.\n"
		hook(t, postPayload(t, root, "post-write", "stele.md", added), checkOff...)
		key, err := loadKey()
		if err != nil {
			t.Fatal(err)
		}
		idx, err := index.Load(root, key)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range idx.Rules {
			found = found || strings.Contains(e.Body, "Name every goroutine")
		}
		if !found {
			t.Error("the index lacks the rule the agent added")
		}
		if got := readFile(t, filepath.Join(root, ".claude", "rules", "stele-rules.md")); !strings.Contains(got, "Name every goroutine") {
			t.Errorf("the rules file lacks the rule the agent added:\n%s", got)
		}
	})
	t.Run("an invalid rule is still reported", func(t *testing.T) {
		root := project(t, checkRules)
		o, out := hook(t, postPayload(t, root, "post-write", "stele.md", checkRules+badRule), checkOff...)
		if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "matches empty text") || !strings.Contains(o.SystemMessage, "cannot be checked") {
			t.Errorf("check off, invalid rule: %s", out)
		}
	})
	t.Run("a stele.md that does not parse is reported, the rules file kept", func(t *testing.T) {
		root := project(t, checkRules)
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile = %d: %s", code, errOut)
		}
		rulesFile := filepath.Join(root, ".claude", "rules", "stele-rules.md")
		before := readFile(t, rulesFile)
		o, out := hook(t, postPayload(t, root, "post-write", "stele.md", "tags: [a]\n"), checkOff...)
		if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "stele.md:1") {
			t.Errorf("check off, parse error: %s, want it named", out)
		}
		if got := readFile(t, rulesFile); got != before {
			t.Errorf("the rules file changed:\n%s", got)
		}
	})
	t.Run("stele failing only warns, even with block_on_error", func(t *testing.T) {
		root := project(t, checkRules)
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"block_on_error": true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		old := keyDir
		keyDir = func() (string, error) { return "", errors.New("no config directory") }
		t.Cleanup(func() { keyDir = old })
		o, out := hook(t, postPayload(t, root, "post-write", "a.go", "password = 1\n"), checkOff...)
		if o.Decision != "" || !strings.Contains(o.SystemMessage, "no config directory") {
			t.Errorf("check off, no key: %s, want a warning and no block", out)
		}
	})
}
