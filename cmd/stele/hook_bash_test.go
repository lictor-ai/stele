package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/install"
)

// bash is a PreToolUse payload for a Bash command run in cwd.
func bash(command, cwd, prompt, agent string) map[string]any {
	return map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": prompt, "agent_id": agent, "cwd": cwd,
		"tool_name": "Bash", "tool_use_id": "toolu_1",
		"tool_input": map[string]any{"command": command, "description": "write a file"},
	}
}

// writeGo writes a.go through Bash, the way agents do it.
const writeGo = "cat > a.go <<'EOF'\npackage a\n\nfunc f(x, y int) bool { return x > y }\nEOF"

func denied(o hookOutput) bool { return o.HookSpecificOutput.PermissionDecision == "deny" }

// A Bash command that writes a file a scoped rule covers is stopped once
// per file and turn, and the agent is sent to Write and Edit (#57).
func TestHookBash(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))

	// 1. A command that writes nothing: no answer, and nothing written.
	if _, out := hook(t, bash("ls -la && go test ./... 2>&1", root, "p1", "")); out != "" {
		t.Errorf("a command that writes nothing: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a command that writes nothing created .stele: %v", err)
	}
	// 3. A file only unscoped rules cover, such as a log, is let through.
	if o, out := hook(t, bash("go test ./... > out.txt", root, "p1", "")); denied(o) {
		t.Errorf("a write to out.txt was stopped: %s", out)
	}

	// 4. The first write of a.go is stopped, with the rules.
	o, _ := hook(t, bash(writeGo, root, "p1", ""))
	if !denied(o) {
		t.Fatalf("the heredoc write went through: %+v", o)
	}
	reason := o.HookSpecificOutput.PermissionDecisionReason
	for _, want := range []string{"a.go", "Write or Edit", "Never build SQL", "Log through the shared logger"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, reason)
		}
	}
	if strings.Contains(reason, "x > y") {
		t.Errorf("reason quotes the heredoc body:\n%s", reason)
	}

	// 6. A parallel call, before the agent can have read it: stopped again.
	advance(100 * time.Millisecond)
	if o, _ := hook(t, bash(writeGo, root, "p1", "")); !denied(o) {
		t.Error("a parallel call went through")
	}
	// 5. The same command run again goes through, unchecked, and says so.
	advance(600 * time.Millisecond)
	o, _ = hook(t, bash(writeGo, root, "p1", ""))
	if denied(o) {
		t.Fatalf("the command run again was stopped: %s", o.HookSpecificOutput.PermissionDecisionReason)
	}
	if !strings.Contains(o.HookSpecificOutput.AdditionalContext, "stele check a.go") {
		t.Errorf("context %q should say to run stele check", o.HookSpecificOutput.AdditionalContext)
	}

	// 8. A subagent has its own record.
	if o, _ := hook(t, bash(writeGo, root, "p1", "sub1")); !denied(o) {
		t.Error("a subagent's first Bash write went through")
	}
	// 7. A new turn starts over.
	advance(time.Minute)
	if o, _ := hook(t, bash(writeGo, root, "p2", "")); !denied(o) {
		t.Error("a new turn's first Bash write went through")
	}
	// 9. The Write the agent makes instead goes through: it has the rules.
	advance(600 * time.Millisecond)
	if o, out := hook(t, write(filepath.Join(root, "a.go"), "p2", "")); denied(o) {
		t.Errorf("the Write after a Bash deny was stopped: %s", out)
	}
	// 14. cd moves where the command writes.
	if o, _ := hook(t, bash("cd internal && echo 'package x' > x.go", root, "p2", "")); !denied(o) || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "internal/x.go") {
		t.Errorf("a write after cd: %+v", o)
	}
}

// A whole payload, as Claude Code sends it, pointed at the project.
func TestHookBashPayload(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	data, err := os.ReadFile(filepath.Join(testdata, "hook", "pre-bash.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p["cwd"] = root
	o, out := hook(t, p)
	if !denied(o) || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "internal/db/query.go") {
		t.Errorf("the recorded payload: %s", out)
	}
}

// bigRules is stele.md with one 3 KB rule per extension, so that four of
// them do not fit in one message.
func bigRules(exts ...string) string {
	var b strings.Builder
	for i, e := range exts {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&b, "scope: [\"**/*.%s\"]\n\nRule for %s files. %s\n", e, e, strings.Repeat("Keep it short. ", 200))
	}
	return b.String()
}

// A deny holds no more rules than fit in Claude Code's 10,000 characters;
// the rest are shown on the next write, not marked shown and lost.
func TestHookFitsTheRules(t *testing.T) {
	exts := []string{"go", "ts", "py", "rs"}
	t.Run("Bash", func(t *testing.T) {
		root := project(t, bigRules(exts...))
		advance := clock(t, time.Now())
		o, _ := hook(t, bash("sed -i s/a/b/ a.go b.ts c.py d.rs", root, "p1", ""))
		reason := o.HookSpecificOutput.PermissionDecisionReason
		if !denied(o) || len(reason) > install.MaxMessage || !strings.Contains(reason, "more rules") {
			t.Fatalf("deny of %d bytes:\n%.300s", len(reason), reason)
		}
		shown := countRules(reason, exts)
		// The Writes the agent makes instead show the rest, file by file.
		advance(time.Second)
		for i, e := range exts {
			o, _ := hook(t, write(filepath.Join(root, string(rune('a'+i))+"."+e), "p1", ""))
			if got := countRules(o.HookSpecificOutput.PermissionDecisionReason, exts); got > 0 {
				shown += got
			}
			advance(time.Second)
		}
		if shown != len(exts) {
			t.Errorf("%d of %d rules shown in all", shown, len(exts))
		}
	})
	t.Run("Write", func(t *testing.T) {
		root := project(t, strings.ReplaceAll(bigRules(exts...), "scope: [\"**/*.", "scope: [\"x.go\", \"**/*."))
		advance := clock(t, time.Now())
		shown := 0
		for range 5 {
			o, _ := hook(t, write(filepath.Join(root, "x.go"), "p1", ""))
			if !denied(o) {
				break
			}
			reason := o.HookSpecificOutput.PermissionDecisionReason
			if len(reason) > install.MaxMessage {
				t.Fatalf("deny of %d bytes", len(reason))
			}
			shown += countRules(reason, exts)
			advance(time.Second)
		}
		if shown != len(exts) {
			t.Errorf("%d of %d rules shown before the write went through", shown, len(exts))
		}
	})
	t.Run("two projects share one budget", func(t *testing.T) {
		root := project(t, bigRules("go", "ts"))
		clock(t, time.Now())
		if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sub", "stele.md"), []byte(bigRules("py", "rs")), 0o644); err != nil {
			t.Fatal(err)
		}
		o, _ := hook(t, bash("sed -i s/a/b/ a.go b.ts sub/c.py sub/d.rs", root, "p1", ""))
		if reason := o.HookSpecificOutput.PermissionDecisionReason; !denied(o) || len(reason) > install.MaxMessage {
			t.Errorf("deny of %d bytes over two projects", len(reason))
		}
	})
}

// countRules counts the big rules a message holds.
func countRules(msg string, exts []string) int {
	n := 0
	for _, e := range exts {
		if strings.Contains(msg, "Rule for "+e+" files.") {
			n++
		}
	}
	return n
}

// 2. A command that writes outside every stele project is left alone.
func TestHookBashOutsideProjects(t *testing.T) {
	project(t, hookRules)
	clock(t, time.Now())
	elsewhere := t.TempDir()
	if _, out := hook(t, bash(writeGo, elsewhere, "p1", "")); out != "" {
		t.Errorf("a write outside every project: %q", out)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".stele")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".stele created outside a project: %v", err)
	}
}

// 10. A Bash command that changes one of stele's own files asks the user,
// as a Write does (#52).
func TestHookBashAsksForOwnFiles(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	// Never the real user's settings.
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("CFG", cfg)
	for command, rel := range map[string]string{
		"echo '{}' > .claude/settings.json":                     ".claude/settings.json",
		"sed -i s/a/b/ .claude/settings.local.json":             ".claude/settings.local.json",
		`echo '{"disableAllHooks": true}' > $CFG/settings.json`: "settings.json",
		"echo '# x' >> stele.md":                                "stele.md",
		"sed -i s/a/b/ stele.json":                              "stele.json",
		"rm .stele/index.json":                                  ".stele/index.json",
		"cp x sub/stele.md":                                     "sub/stele.md",
		"mv stele.lock old.lock":                                "stele.lock",
		"truncate -s 0 stele.lock":                              "stele.lock",
		"cd sub && touch x > stele.md":                          "sub/stele.md",
		"echo x > .claude/rules/stele-rules.md":                 ".claude/rules/stele-rules.md",
		"rm .claude/rules/stele-rules.md":                       ".claude/rules/stele-rules.md",
	} {
		o, out := hook(t, bash(command, root, "p1", ""))
		if !asks(t, o, rel) {
			t.Errorf("%q: want an ask naming %s, got %s", command, rel, out)
		}
	}
	// With a file the rules cover in the same command, the deny wins.
	if o, _ := hook(t, bash("echo x >> stele.md; "+writeGo, root, "p1", "")); !denied(o) {
		t.Errorf("an own file and a.go: %+v", o)
	}
	// A link from outside every project into stele.md (last: link skips the
	// test where links need a privilege).
	outside := t.TempDir()
	link(t, filepath.Join(root, "stele.md"), filepath.Join(outside, "rules.md"), false)
	if o, out := hook(t, bash("echo x > rules.md", outside, "p1", "")); !asks(t, o, "stele.md") {
		t.Errorf("a link to stele.md: %s", out)
	}
}

// 11. With backstop off the hook never stops a command: the agent is told
// the file is unchecked. An own file is still asked about.
func TestHookBashBackstopOff(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"backstop": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o, out := hook(t, bash(writeGo, root, "p1", ""))
	if denied(o) || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "stele check a.go") {
		t.Errorf("backstop off: %s", out)
	}
	if o, out := hook(t, bash("echo x >> stele.md", root, "p1", "")); !asks(t, o, "stele.md") {
		t.Errorf("backstop off, own file: %s", out)
	}
}

// 12. When stele itself fails it cannot tell a log from a source file, so a
// Bash command is let through with a warning, even with block_on_error.
func TestHookBashFailurePolicy(t *testing.T) {
	clock(t, time.Now())
	noKey := func(t *testing.T) {
		old := keyDir
		keyDir = func() (string, error) { return "", errors.New("no config directory") }
		t.Cleanup(func() { keyDir = old })
	}
	t.Run("default: allow and warn", func(t *testing.T) {
		root := project(t, hookRules)
		noKey(t)
		if o, out := hook(t, bash(writeGo, root, "p1", "")); denied(o) || !strings.Contains(o.SystemMessage, "no config directory") {
			t.Errorf("want a warning, got %s", out)
		}
	})
	t.Run("block_on_error: still only a warning", func(t *testing.T) {
		root := project(t, hookRules)
		noKey(t)
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"block_on_error": true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{writeGo, "go vet ./... > vet.txt"} {
			if o, out := hook(t, bash(command, root, "p1", "")); denied(o) || !strings.Contains(o.SystemMessage, "no config directory") {
				t.Errorf("%q: want a warning, got %s", command, out)
			}
		}
	})
	t.Run("a command that writes nothing never reads the key", func(t *testing.T) {
		root := project(t, hookRules)
		noKey(t)
		if _, out := hook(t, bash("ls", root, "p1", "")); out != "" {
			t.Errorf("ls: %q", out)
		}
	})
}

// 13. A command writing into two projects is stopped once, naming both, and
// each project keeps its own record.
func TestHookBashTwoRoots(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "stele.md"), []byte("scope: [\"**/*.go\"]\n\nSub rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, out := hook(t, bash(writeGo+"\necho 'package b' > sub/b.go", root, "p1", ""))
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if !denied(o) || !strings.Contains(reason, "a.go") || !strings.Contains(reason, "b.go") || !strings.Contains(reason, "Sub rule.") {
		t.Fatalf("two roots: %s", out)
	}
	for _, dir := range []string{root, sub} {
		if _, err := os.Stat(filepath.Join(dir, ".stele", "state")); err != nil {
			t.Errorf("no session state in %s: %v", dir, err)
		}
	}
}

// 15. Git Bash's /c/... paths are Windows paths.
func TestHookBashMSYSPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Git Bash paths are a Windows matter")
	}
	root := project(t, hookRules)
	clock(t, time.Now())
	msys := "/" + strings.ToLower(root[:1]) + filepath.ToSlash(root[2:])
	if o, out := hook(t, bash("echo x > "+msys+"/a.go", t.TempDir(), "p1", "")); !denied(o) {
		t.Errorf("a /c/... target went through: %s", out)
	}
}

// 16. A bug in stele lets the command through with a warning.
func TestHookBashRecoversFromPanic(t *testing.T) {
	root := project(t, hookRules)
	old := now
	now = func() time.Time { panic("boom") }
	t.Cleanup(func() { now = old })
	if o, _ := hook(t, bash(writeGo, root, "p1", "")); denied(o) || !strings.Contains(o.SystemMessage, "boom") {
		t.Errorf("after a panic: %+v", o)
	}
}

// 17. Nothing runs after a Bash command.
func TestHookBashAfter(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	p := bash(writeGo, root, "p1", "")
	p["hook_event_name"] = "PostToolUse"
	p["tool_response"] = map[string]any{"stdout": "", "stderr": "", "interrupted": false}
	if _, out := hook(t, p); out != "" {
		t.Errorf("PostToolUse after Bash: %q", out)
	}
}
