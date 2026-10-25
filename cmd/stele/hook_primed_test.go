package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSession runs session s1's SessionStart hook with Claude Code in cwd.
// The event and agent arguments are kept for the SubagentStart cases, which
// startSubagent covers with an agent type.
func startSession(t *testing.T, event, cwd, agent, source string) {
	t.Helper()
	if event == "SubagentStart" {
		startSubagent(t, cwd, agent, "general-purpose")
		return
	}
	p := sessionPayload(t, event, cwd)
	p["session_id"], p["source"] = "s1", source
	hook(t, p)
}

// startSubagent runs session s1's SubagentStart hook for agent of agentType
// ("" leaves agent_type out), with the hook's cwd at cwd.
func startSubagent(t *testing.T, cwd, agent, agentType string) {
	t.Helper()
	p := sessionPayload(t, "SubagentStart", cwd)
	p["session_id"] = "s1"
	delete(p, "source")
	if agent != "" {
		p["agent_id"] = agent
	}
	if agentType != "" {
		p["agent_type"] = agentType
	}
	hook(t, p)
}

// primedProject is a stele project with its rules file written, as after
// stele init or stele compile.
func primedProject(t *testing.T) string {
	t.Helper()
	root := project(t, hookRules)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d: %s", code, errOut)
	}
	clock(t, time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC))
	return root
}

func writeRules(t *testing.T, root, rules string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The rules the session loaded at its start are not shown again; a rule
// changed since is (#68).
func TestHookPrimedRules(t *testing.T) {
	root := primedProject(t)
	goFile := filepath.Join(root, "a.go")
	startSession(t, "SessionStart", root, "", "startup")

	if o, _ := hook(t, write(goFile, "p1", "")); denied(o) {
		t.Fatalf("a write needing only loaded rules was denied: %s", o.HookSpecificOutput.PermissionDecisionReason)
	}
	if o, _ := hook(t, write(goFile, "p2", "")); denied(o) {
		t.Fatal("a new turn denied loaded rules: they stay loaded for the session")
	}

	// Change the scoped rule: the session loaded the old text.
	writeRules(t, root, strings.Replace(hookRules, "string concatenation", "hand", 1))
	o, _ := hook(t, write(goFile, "p2", ""))
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if !denied(o) || !strings.Contains(reason, "Never build SQL by hand") || strings.Contains(reason, "shared logger") {
		t.Errorf("after a rule changed: decision %q, reason %q; want a deny with only the changed rule",
			o.HookSpecificOutput.PermissionDecision, reason)
	}

	// A subagent with no SubagentStart loaded nothing stele knows of.
	if o, _ := hook(t, write(goFile, "p2", "sub")); !denied(o) {
		t.Error("a subagent with no recorded start went through: it has its own record")
	}
	// A subagent started now loads the current file, which the hook rewrote.
	startSession(t, "SubagentStart", root, "sub2", "")
	if o, _ := hook(t, write(goFile, "p2", "sub2")); denied(o) {
		t.Errorf("a subagent that loaded the current file was denied: %s", o.HookSpecificOutput.PermissionDecisionReason)
	}
}

// At session start the hook records the file as the session read it, then
// rewrites it: recording after the rewrite would prime rules the session
// never saw.
func TestHookPrimedRecordsThenRewrites(t *testing.T) {
	root := primedProject(t)
	writeRules(t, root, strings.Replace(hookRules, "string concatenation", "hand", 1))
	startSession(t, "SessionStart", root, "", "startup")

	data, err := os.ReadFile(filepath.Join(root, ".claude", "rules", "stele-rules.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Never build SQL by hand") {
		t.Error("the stale rules file was not rewritten after the session start")
	}
	o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if !denied(o) || !strings.Contains(reason, "by hand") || strings.Contains(reason, "shared logger") {
		t.Errorf("decision %q, reason %q; want a deny with only the rule the session read in its old form",
			o.HookSpecificOutput.PermissionDecision, reason)
	}

	// After a compaction Claude Code reads the file again: the current one.
	startSession(t, "SessionStart", root, "", "compact")
	if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p2", "")); denied(o) {
		t.Errorf("after compact: denied %s, want the reloaded rules counted", o.HookSpecificOutput.PermissionDecisionReason)
	}
}

// Nothing is primed when stele cannot tell what the session loaded.
func TestHookPrimedNothing(t *testing.T) {
	t.Run("started in a subdirectory", func(t *testing.T) {
		root := primedProject(t)
		sub := filepath.Join(root, "internal")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		startSession(t, "SessionStart", sub, "", "startup")
		if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "")); !denied(o) {
			t.Error("a session started in a subdirectory was primed: it may not have loaded the root's file")
		}
	})
	t.Run("a rules file that is not stele's", func(t *testing.T) {
		root := primedProject(t)
		file := filepath.Join(root, ".claude", "rules", "stele-rules.md")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// Same blocks, without stele's header.
		_, rest, _ := strings.Cut(string(data), "\n")
		if err := os.WriteFile(file, []byte(rest), 0o644); err != nil {
			t.Fatal(err)
		}
		startSession(t, "SessionStart", root, "", "startup")
		if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "")); !denied(o) {
			t.Error("rules were primed from a file stele did not write")
		}
	})
	t.Run("a subagent start with no agent id", func(t *testing.T) {
		root := primedProject(t)
		startSession(t, "SessionStart", filepath.Join(root, "nowhere"), "", "startup") // primes nothing
		startSession(t, "SubagentStart", root, "", "")
		if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "")); !denied(o) {
			t.Error("a SubagentStart with no agent id primed the main agent")
		}
	})
}

// Claude Code reloads the rules file from the directory it was launched in,
// while the hook's cwd follows the agent's cd. So a compaction, clear or
// subagent start primes only the root a startup or resume marked as the
// session's launch root.
func TestHookPrimedLaunchRoot(t *testing.T) {
	// mono is a git repository that is not a stele project; svc, below it, is
	// one with its own rules file.
	setup := func(t *testing.T) (mono, svc string) {
		t.Helper()
		mono = project(t, "")
		svc = filepath.Join(mono, "svc")
		if err := os.Mkdir(svc, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRules(t, svc, hookRules)
		t.Chdir(svc)
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile = %d: %s", code, errOut)
		}
		clock(t, time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC))
		return mono, svc
	}
	svcWrite := func(t *testing.T, svc, agent string) hookOutput {
		t.Helper()
		o, _ := hook(t, write(filepath.Join(svc, "a.go"), "p1", agent))
		return o
	}

	t.Run("launched above, compacted after cd into svc", func(t *testing.T) {
		mono, svc := setup(t)
		startSession(t, "SessionStart", mono, "", "startup")
		startSession(t, "SessionStart", svc, "", "compact")
		if !denied(svcWrite(t, svc, "")) {
			t.Error("svc was primed at compact, though Claude Code reloads the launch directory's rules")
		}
	})
	t.Run("launched above, a clear after cd into svc", func(t *testing.T) {
		mono, svc := setup(t)
		startSession(t, "SessionStart", mono, "", "startup")
		startSession(t, "SessionStart", svc, "", "clear")
		if !denied(svcWrite(t, svc, "")) {
			t.Error("svc was primed at clear")
		}
	})
	t.Run("launched above, a subagent started with cwd svc", func(t *testing.T) {
		mono, svc := setup(t)
		startSession(t, "SessionStart", mono, "", "startup")
		startSubagent(t, svc, "sub", "general-purpose")
		if !denied(svcWrite(t, svc, "sub")) {
			t.Error("svc was primed for a subagent of a session launched elsewhere")
		}
	})
	t.Run("launched in svc", func(t *testing.T) {
		_, svc := setup(t)
		startSession(t, "SessionStart", svc, "", "startup")
		if o := svcWrite(t, svc, ""); denied(o) {
			t.Errorf("a session launched in svc was not primed: %s", o.HookSpecificOutput.PermissionDecisionReason)
		}
	})
	t.Run("launched in svc, then compacted", func(t *testing.T) {
		_, svc := setup(t)
		startSession(t, "SessionStart", svc, "", "startup")
		startSession(t, "SessionStart", svc, "", "compact")
		if o := svcWrite(t, svc, ""); denied(o) {
			t.Errorf("a compaction in the launch root primed nothing: %s", o.HookSpecificOutput.PermissionDecisionReason)
		}
	})
	t.Run("resumed in svc", func(t *testing.T) {
		_, svc := setup(t)
		startSession(t, "SessionStart", svc, "", "resume")
		if o := svcWrite(t, svc, ""); denied(o) {
			t.Errorf("a session resumed in svc was not primed: %s", o.HookSpecificOutput.PermissionDecisionReason)
		}
	})
}

// Only subagents measured to load .claude/rules are primed: general-purpose
// does; Explore and Plan do not (Claude Code 2.1.310, 2026-10-25).
func TestHookPrimedSubagentTypes(t *testing.T) {
	for _, tt := range []struct {
		agentType string
		primed    bool
	}{
		{"general-purpose", true},
		{"Explore", false},
		{"Plan", false},
		{"my-custom-agent", false},
		{"", false},
	} {
		t.Run("type "+tt.agentType, func(t *testing.T) {
			root := primedProject(t)
			startSession(t, "SessionStart", root, "", "startup")
			startSubagent(t, root, "sub", tt.agentType)
			o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "sub"))
			if denied(o) == tt.primed {
				t.Errorf("agent_type %q: denied = %v, want primed = %v", tt.agentType, denied(o), tt.primed)
			}
		})
	}
}

// A Bash command writing a file with rules is still stopped once when every
// rule is loaded: the write would go unchecked. No rule is listed.
func TestHookPrimedBash(t *testing.T) {
	root := primedProject(t)
	startSession(t, "SessionStart", root, "", "startup")
	o, _ := hook(t, bash(writeGo, root, "p1", ""))
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if !denied(o) || strings.Contains(reason, "Never build SQL") || strings.Contains(reason, "shared logger") {
		t.Errorf("decision %q, reason %q; want a deny listing no rule", o.HookSpecificOutput.PermissionDecision, reason)
	}
}

// The record of what each agent loaded is replaced at each start, and capped:
// past maxPrimed agents the other subagents' records go, never the main
// agent's or the one starting now.
func TestWithPrimed(t *testing.T) {
	m := map[string]map[string]string{"": {"a": "h1"}}
	if got := withPrimed(m, "", map[string]string{"b": "h2"}); len(got[""]) != 1 || got[""]["b"] != "h2" {
		t.Errorf("replace = %v, want only b", got)
	}
	if m[""]["a"] != "h1" {
		t.Error("withPrimed changed its input")
	}
	if got := withPrimed(m, "", nil); got != nil {
		t.Errorf("nothing loaded = %v, want no record", got)
	}
	for i := range maxPrimed + 5 {
		m = withPrimed(m, "sub"+strings.Repeat("x", i), map[string]string{"a": "h"})
	}
	if len(m) > maxPrimed {
		t.Errorf("%d records, want at most %d", len(m), maxPrimed)
	}
	if m[""] == nil || m["sub"+strings.Repeat("x", maxPrimed+4)] == nil {
		t.Error("the cap dropped the main agent or the agent starting now")
	}
}

// A panic while recording the session start is recovered, like any other.
func TestHookPrimedRecoversFromPanic(t *testing.T) {
	root := primedProject(t)
	old := now
	now = func() time.Time { panic("boom") }
	t.Cleanup(func() { now = old })
	startSession(t, "SessionStart", root, "", "startup") // hook fails the test on a non-zero exit
}
