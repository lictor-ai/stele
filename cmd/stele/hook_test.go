package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hookRules = `scope: ["**/*.go"]

Never build SQL by string concatenation.

---

Log through the shared logger.
`

// hookOutput is Claude Code's view of the hook's stdout.
type hookOutput struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	SystemMessage string `json:"systemMessage"`
}

// clock sets the hook's clock to start, and returns a way to move it.
func clock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	cur := start
	old := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = old })
	return func(d time.Duration) { cur = cur.Add(d) }
}

// hook runs stele hook on a PreToolUse payload for file.
func hook(t *testing.T, payload map[string]any) (hookOutput, string) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	old := stdin
	stdin = bytes.NewReader(data)
	defer func() { stdin = old }()
	code, out, errOut := runCmd("hook")
	if code != exitOK {
		t.Fatalf("hook exit = %d, stderr %q: the hook must always exit 0", code, errOut)
	}
	var o hookOutput
	if out != "" {
		if err := json.Unmarshal([]byte(out), &o); err != nil {
			t.Fatalf("hook stdout is not JSON: %q", out)
		}
	}
	return o, out
}

// A bug in stele must not block the write: Go exits 2 on a panic, and exit 2
// blocks a PreToolUse call whatever stdout says.
func TestHookRecoversFromPanic(t *testing.T) {
	root := project(t, hookRules)
	old := now
	now = func() time.Time { panic("boom") }
	t.Cleanup(func() { now = old })

	o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "" || !strings.Contains(o.SystemMessage, "boom") {
		t.Errorf("after a panic: decision %q, warning %q; want no decision and a warning naming the panic",
			o.HookSpecificOutput.PermissionDecision, o.SystemMessage)
	}
}

func write(file, prompt, agent string) map[string]any {
	return map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": prompt, "agent_id": agent,
		"tool_name": "Write", "tool_input": map[string]any{"file_path": file, "content": "x"},
	}
}

// A clock moved back must not deny every write until it catches up (#53).
func TestHookClockMovedBack(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", "")); o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("first write: decision %q, want deny", o.HookSpecificOutput.PermissionDecision)
	}
	advance(-time.Hour)
	if _, out := hook(t, write(filepath.Join(root, "b.go"), "p1", "")); out != "" {
		t.Errorf("after the clock moved back: stdout %q, want nothing", out)
	}
}

func TestHookShowsRules(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	goFile := filepath.Join(root, "internal", "db", "query.go")

	// First write of the turn: denied, with both rules (one scoped, one not).
	o, _ := hook(t, write(goFile, "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("first write: decision %q, want deny", o.HookSpecificOutput.PermissionDecision)
	}
	reason := o.HookSpecificOutput.PermissionDecisionReason
	for _, want := range []string{"internal/db/query.go", "Never build SQL", "Log through the shared logger", "retry"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, reason)
		}
	}

	// A parallel write to another file in the same batch is denied too.
	advance(time.Second)
	otherFile := filepath.Join(root, "internal", "db", "other.go")
	if o, _ := hook(t, write(otherFile, "p1", "")); o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Error("a write in the same batch went through before the rules were read")
	}

	// The retry of the denied file goes through within the window, with no
	// output at all: a model can retry in under 3 s.
	advance(time.Second)
	if _, out := hook(t, write(goFile, "p1", "")); out != "" {
		t.Errorf("retry within the window: stdout %q, want nothing", out)
	}

	// So does a retry after the window.
	advance(10 * time.Second)
	if _, out := hook(t, write(otherFile, "p1", "")); out != "" {
		t.Errorf("retry: stdout %q, want nothing", out)
	}

	// A markdown file needs only the unscoped rule, already shown.
	if _, out := hook(t, write(filepath.Join(root, "README.md"), "p1", "")); out != "" {
		t.Errorf("README in the same turn: stdout %q, want nothing", out)
	}

	// A subagent has its own context, so it is shown the rules again.
	o, _ = hook(t, write(goFile, "p1", "sub1"))
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Error("a subagent wrote without being shown the rules")
	}

	// A new turn shows them again.
	advance(time.Minute)
	o, _ = hook(t, write(filepath.Join(root, "README.md"), "p2", ""))
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatal("a new turn did not show the rules again")
	}
	if strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "Never build SQL") {
		t.Error("a scoped .go rule was shown for a README")
	}
}

func TestHookAllows(t *testing.T) {
	clock(t, time.Now())
	t.Run("file outside the project", func(t *testing.T) {
		project(t, hookRules)
		outside := filepath.Join(t.TempDir(), "x.go")
		if _, out := hook(t, write(outside, "p1", "")); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("no stele in the repository", func(t *testing.T) {
		root := project(t, "")
		if _, out := hook(t, write(filepath.Join(root, "x.go"), "p1", "")); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
		if _, err := os.Stat(filepath.Join(root, "stele.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Error("the hook wrote stele.lock into a repository that does not use stele")
		}
	})
	t.Run("backstop off", func(t *testing.T) {
		root := project(t, hookRules)
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"backstop": false}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, out := hook(t, write(filepath.Join(root, "x.go"), "p1", "")); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("not a write", func(t *testing.T) {
		project(t, hookRules)
		payload := map[string]any{"hook_event_name": "PreToolUse", "session_id": "s1", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}
		if _, out := hook(t, payload); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("garbage on stdin", func(t *testing.T) {
		project(t, hookRules)
		old := stdin
		stdin = strings.NewReader("not json")
		defer func() { stdin = old }()
		if code, out, _ := runCmd("hook"); code != exitOK || out != "" {
			t.Errorf("hook = %d, %q; want 0 and nothing", code, out)
		}
	})
}

// A rule whose scope excludes a file is neither shown before a write to it
// nor checked after.
func TestHookScopeExclusion(t *testing.T) {
	const rules = "scope: [\"internal/**/*.go\", \"!**/*_test.go\"]\ncheck: regex\npattern: fmt\\.Print\n\nNo printing under internal/.\n"
	clock(t, time.Now())
	root := project(t, rules)
	if _, out := hook(t, write(filepath.Join(root, "internal", "x_test.go"), "p1", "")); out != "" {
		t.Errorf("before a write to an excluded file: stdout %q, want nothing", out)
	}
	if _, out := postHook(t, root, "post-write", "internal/x_test.go", "package x\n\nfunc f() { fmt.Println() }\n"); out != "" {
		t.Errorf("after a write to an excluded file: stdout %q, want nothing", out)
	}
	if o, _ := postHook(t, root, "post-write", "internal/x.go", "package x\n\nfunc f() { fmt.Println() }\n"); o.Decision != "block" {
		t.Errorf("after a write to an included file: decision %q, want block", o.Decision)
	}
}

// A stele.md that stops parsing keeps the last good rules in force, and the
// agent is told what is wrong.
func TestHookKeepsLastGoodRules(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	goFile := filepath.Join(root, "a.go")
	if o, _ := hook(t, write(goFile, "p1", "")); o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatal("setup: first write not denied")
	}
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte("tags: [a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, _ := hook(t, write(goFile, "p2", ""))
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatal("broken stele.md: the rules stopped being enforced")
	}
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if !strings.Contains(reason, "Never build SQL") || !strings.Contains(reason, "stele.md:1") {
		t.Errorf("reason should carry the last good rules and the parse error:\n%s", reason)
	}
}

func TestHookFailurePolicy(t *testing.T) {
	clock(t, time.Now())
	noKey := func(t *testing.T) {
		old := keyDir
		keyDir = func() (string, error) { return "", errors.New("no config directory") }
		t.Cleanup(func() { keyDir = old })
	}
	t.Run("default: allow and warn the user", func(t *testing.T) {
		root := project(t, hookRules)
		noKey(t)
		o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
		if o.HookSpecificOutput.PermissionDecision == "deny" {
			t.Error("denied although block_on_error is off")
		}
		if !strings.Contains(o.SystemMessage, "no config directory") {
			t.Errorf("warning %q should name the failure", o.SystemMessage)
		}
	})
	t.Run("block_on_error: block and say why", func(t *testing.T) {
		root := project(t, hookRules)
		noKey(t)
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"block_on_error": true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
		if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "no config directory") {
			t.Errorf("want a deny naming the failure, got %+v", o)
		}
	})
	t.Run("no session id: the record cannot be saved", func(t *testing.T) {
		root := project(t, hookRules)
		payload := write(filepath.Join(root, "a.go"), "p1", "")
		delete(payload, "session_id")
		o, _ := hook(t, payload)
		if o.HookSpecificOutput.PermissionDecision == "deny" {
			t.Error("denied a write whose record could not be saved: it would be denied forever")
		}
		if o.SystemMessage == "" {
			t.Error("no warning for the user")
		}
	})
}
