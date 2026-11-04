package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/journal"
)

// codexOutput is Codex's view of the hook's stdout.
type codexOutput struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	SystemMessage      string `json:"systemMessage"`
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// codexPayload is the payload Codex 0.166.0 sent, recorded in
// testdata/hook/codex/<fixture>.json, its cwd moved to cwd; command, when not
// "", replaces its tool_input's command, a patch or a shell line.
func codexPayload(t *testing.T, fixture, cwd, command string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, "hook", "codex", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p["cwd"] = cwd
	if command != "" {
		p["tool_input"].(map[string]any)["command"] = command
	}
	return p
}

// codexHook runs stele hook --agent=codex on payload.
func codexHook(t *testing.T, payload map[string]any, args ...string) (codexOutput, string) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	old := stdin
	stdin = bytes.NewReader(data)
	defer func() { stdin = old }()
	code, out, errOut := runCmd(append([]string{"hook", agentFlag + "codex"}, args...)...)
	if code != exitOK {
		t.Fatalf("hook exit = %d, stderr %q: the hook must always exit 0", code, errOut)
	}
	var o codexOutput
	if out != "" {
		dec := json.NewDecoder(strings.NewReader(out))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o); err != nil {
			t.Fatalf("hook stdout is not Codex's JSON: %q: %v", out, err)
		}
	}
	return o, out
}

const twoFilePatch = "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n*** Update File: README.md\n@@\n-old\n+new\n*** End Patch"

// One apply_patch to two files is one deny, with the rules of both, and its
// retry goes through.
func TestHookCodexMultiFileDeny(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	p := codexPayload(t, "pre-patch-multi", root, twoFilePatch)
	o, _ := codexHook(t, p)
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("decision %q, want deny", o.HookSpecificOutput.PermissionDecision)
	}
	for _, want := range []string{"internal/db.go, README.md", "Never build SQL", "Log through the shared logger", "apply_patch only, not through shell"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, reason)
		}
	}
	advance(2 * time.Second)
	if _, out := codexHook(t, p); out != "" {
		t.Errorf("the retry of the same patch: stdout %q, want nothing", out)
	}
	// The log tells the agents apart (D69).
	var denied []logLine
	for _, l := range readLog(t, root) {
		if l.Kind == journal.Denied {
			denied = append(denied, l)
		}
	}
	if len(denied) != 1 || denied[0].Agent != "codex" || len(denied[0].Files) != 2 {
		t.Errorf("denied lines = %+v", denied)
	}
}

// Codex cannot ask the user: a patch that touches stele's own files is
// refused, and the agent told to leave it to the user.
func TestHookCodexOwnFileRefused(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	for _, patch := range []string{
		"*** Begin Patch\n*** Delete File: stele.md\n*** End Patch",
		"*** Begin Patch\n*** Add File: .codex/hooks.json\n+{}\n*** End Patch",
		"*** Begin Patch\n*** Update File: a.go\n*** Move to: stele.json\n@@\n+x\n*** End Patch",
	} {
		o, _ := codexHook(t, codexPayload(t, "pre-patch-add", root, patch))
		h := o.HookSpecificOutput
		if h.PermissionDecision != "deny" || !strings.Contains(h.PermissionDecisionReason, "does not let Codex change it") {
			t.Errorf("%q: %+v, want a refusal", patch, h)
		}
	}
}

func TestHookCodexPostBlocksPerFile(t *testing.T) {
	root := project(t, checkRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("package x\n\nvar password = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := codexPayload(t, "post-patch-multi", root,
		"*** Begin Patch\n*** Add File: a.go\n+package x\n+\n+var password = 1\n*** Add File: b.go\n+package x\n+\n+var password = 1\n*** Delete File: c.go\n*** End Patch")
	p["tool_response"] = "Exit code: 0\nWall time: 0.1 seconds\nOutput:\nSuccess. Updated the following files:\nA a.go\nA b.go\nD c.go\n"
	o, _ := codexHook(t, p)
	if o.Decision != "block" {
		t.Fatalf("decision %q, want block", o.Decision)
	}
	for _, want := range []string{"a.go:3:5:", "b.go:3:5:", "was saved", "apply_patch only"} {
		if !strings.Contains(o.Reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, o.Reason)
		}
	}
	// A patch that failed wrote nothing: nothing to check.
	p["tool_response"] = "Exit code: 1\nWall time: 0.1 seconds\nOutput:\nfailed\n"
	if _, out := codexHook(t, p); out != "" {
		t.Errorf("after a failed patch: stdout %q", out)
	}
}

// A shell command that writes a file a scoped rule covers is stopped, as for
// Claude Code; the session context names apply_patch.
func TestHookCodexShellAndSessionStart(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	o, _ := codexHook(t, codexPayload(t, "pre-bash-powershell", root, "echo x > internal/db.go"))
	if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "apply_patch instead") {
		t.Errorf("a shell write: %+v", o.HookSpecificOutput)
	}
	o, _ = codexHook(t, codexPayload(t, "session-start", root, ""))
	if o.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "apply_patch") {
		t.Errorf("session start: %+v", o.HookSpecificOutput)
	}
	// Events stele does not answer say nothing.
	for _, f := range []string{"stop", "user-prompt-submit", "post-bash"} {
		if _, out := codexHook(t, codexPayload(t, f, root, "")); out != "" {
			t.Errorf("%s: stdout %q", f, out)
		}
	}
}

// stele init --agent=codex writes Codex's hooks and AGENTS.md's section,
// leaves the user's text alone, and a startup session then loads the rules:
// its first write shows none again.
func TestCodexInitAndPrimedSession(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no codex: init says so, and goes on
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Mine\n\nUse tabs.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd("init", "--agent=codex")
	if code != exitOK {
		t.Fatalf("init = %d: %s %s", code, out, errOut)
	}
	for _, want := range []string{"added stele hooks (PreToolUse, PostToolUse, SessionStart) to " + filepath.Join(".codex", "hooks.json"), "wrote AGENTS.md", "Codex hooks: remind on, check on"} {
		if !strings.Contains(out, want) {
			t.Errorf("init stdout lacks %q:\n%s", want, out)
		}
	}
	// No codex on PATH: init says what it needs, and goes on.
	for _, want := range []string{"approve stele's hooks", "/hooks", "codex was not found"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("init stderr lacks %q:\n%s", want, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error("a Codex-only init made .claude/")
	}
	agents, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.HasPrefix(string(agents), "# Mine\n\nUse tabs.\n\n<!-- stele:begin") || !strings.Contains(string(agents), "Never build SQL") {
		t.Errorf("AGENTS.md:\n%s", agents)
	}

	if _, out := codexHook(t, codexPayload(t, "session-start", root, "")); !strings.Contains(out, "apply_patch") {
		t.Fatalf("session start: %q", out)
	}
	p := codexPayload(t, "pre-patch-add", root, "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n*** End Patch")
	if _, out := codexHook(t, p); out != "" {
		t.Errorf("a write after a primed startup: stdout %q, want nothing", out)
	}

	// Uninstall takes stele's part out, and leaves the user's.
	code, out, errOut = runCmd("uninstall")
	if code != exitOK {
		t.Fatalf("uninstall = %d: %s %s", code, out, errOut)
	}
	agents, _ = os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(agents) != "# Mine\n\nUse tabs.\n" {
		t.Errorf("AGENTS.md after uninstall: %q", agents)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); err == nil {
		t.Error(".stele/ left after the last agent's hooks went")
	}
}

// An agent's edit inside AGENTS.md's section is undone by the next hook,
// which compiles: the section is stele's.
func TestHookCodexSectionRestored(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	t.Setenv("PATH", t.TempDir())
	if code, out, errOut := runCmd("init", "--agent=codex"); code != exitOK {
		t.Fatalf("init = %d: %s %s", code, out, errOut)
	}
	file := filepath.Join(root, "AGENTS.md")
	data, _ := os.ReadFile(file)
	edited := strings.Replace(string(data), "Never build SQL by string concatenation.", "Build SQL as you like.", 1)
	edited += "My own line.\n"
	if err := os.WriteFile(file, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	codexHook(t, codexPayload(t, "pre-patch-add", root, "*** Begin Patch\n*** Add File: x.go\n+package x\n*** End Patch"))
	data, _ = os.ReadFile(file)
	if !strings.Contains(string(data), "Never build SQL by string concatenation.") || !strings.HasSuffix(string(data), "My own line.\n") {
		t.Errorf("AGENTS.md after the hook:\n%s", data)
	}
}

// A Claude Code project never gets an AGENTS.md it did not ask for.
func TestCompileWritesOnlyActiveAgents(t *testing.T) {
	root := project(t, hookRules)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		t.Error("compile wrote AGENTS.md for a project that does not use Codex")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "rules", "stele-rules.md")); err != nil {
		t.Errorf("the default agent's rules file: %v", err)
	}
}

// A write by one agent to another's settings still asks, or is refused.
func TestGuardCoversEveryAgentsSettings(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	o, _ := hook(t, write(filepath.Join(root, ".codex", "hooks.json"), "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("Claude Code writing .codex/hooks.json: %+v", o.HookSpecificOutput)
	}
	co, _ := codexHook(t, codexPayload(t, "pre-patch-add", root, "*** Begin Patch\n*** Add File: .claude/settings.json\n+{}\n*** End Patch"))
	if co.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("Codex writing .claude/settings.json: %+v", co.HookSpecificOutput)
	}
	// AGENTS.md is the user's: a write to it asks no one.
	co, _ = codexHook(t, codexPayload(t, "pre-patch-add", root, "*** Begin Patch\n*** Add File: AGENTS.md\n+mine\n*** End Patch"))
	if strings.Contains(co.HookSpecificOutput.PermissionDecisionReason, "stele's own files") {
		t.Errorf("a write to AGENTS.md was refused: %+v", co.HookSpecificOutput)
	}
}
