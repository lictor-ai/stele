package functional

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Codex, through the built binary: the hooks stele init writes into
// .codex/hooks.json, run on payloads in the shape Codex 0.166.0 sends
// (cmd/stele/testdata/hook/codex).

const codexRules = `scope: ["**/*.go"]
check: regex
pattern: password\s*=

Never put credentials in code: password = ...

---

scope: ["docs/**"]

Write docs in plain words.
`

// useCodexHook makes p.hook run stele hook with the arguments stele init
// wrote into .codex/hooks.json, as Codex would.
func (p *project) useCodexHook() {
	p.t.Helper()
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(p.read(".codex/hooks.json")), &s); err != nil {
		p.t.Fatal(err)
	}
	words := strings.Fields(s.Hooks["PreToolUse"][0].Hooks[0].Command)
	if len(words) < 3 || words[0] != "stele" || words[1] != "hook" {
		p.t.Fatalf("the PreToolUse command is %q", words)
	}
	p.hookArgs = words[2:]
}

// codexSessions are each project's session id, a UUIDv7 as Codex's are,
// for a session that began a minute after its first event: well after init
// wrote AGENTS.md, as stele needs to count it as read.
var codexSessions = map[string]string{}

func codexSession(dir string) string {
	if _, ok := codexSessions[dir]; !ok {
		u := uuid.Must(uuid.NewV7())
		ms := time.Now().Add(time.Minute).UnixMilli()
		for i := 5; i >= 0; i-- {
			u[i] = byte(ms)
			ms >>= 8
		}
		codexSessions[dir] = u.String()
	}
	return codexSessions[dir]
}

// codexEvent is a Codex hook payload: an apply_patch (patch), a shell
// command (shell), or neither, for a session start.
func (p *project) codexEvent(event, turn, patch, shell, resp string) []byte {
	p.t.Helper()
	m := map[string]any{
		"session_id": codexSession(p.dir), "turn_id": turn, "transcript_path": nil, "cwd": p.dir,
		"hook_event_name": event, "model": "gpt-5.6-luna", "permission_mode": "bypassPermissions",
	}
	switch {
	case event == "SessionStart":
		m["source"] = "startup"
		delete(m, "turn_id")
	case shell != "":
		m["tool_name"], m["tool_input"], m["tool_use_id"] = "Bash", map[string]any{"command": shell}, "exec-1"
	default:
		m["tool_name"], m["tool_input"], m["tool_use_id"] = "apply_patch", map[string]any{"command": patch}, "exec-1"
	}
	if resp != "" {
		m["tool_response"] = resp
	}
	data, err := json.Marshal(m)
	if err != nil {
		p.t.Fatal(err)
	}
	return data
}

func codexInit(t *testing.T, args ...string) *project {
	t.Helper()
	p := newProject(t, codexRules)
	p.env = append(p.env, "CODEX_HOME="+t.TempDir())
	p.write("AGENTS.md", "# Notes\n\nRun go test.\n")
	p.mustStele(0, append([]string{"init", "--agent=codex"}, args...)...)
	p.useCodexHook()
	return p
}

func TestCodexInitWritesHooksAndSection(t *testing.T) {
	p := codexInit(t)
	hooks := p.read(".codex/hooks.json")
	for _, want := range []string{`"PreToolUse"`, `"^(apply_patch|Bash)$"`, `"PostToolUse"`, `"^(apply_patch|Bash)$"`, `"SessionStart"`, `"stele hook --agent=codex"`} {
		if !strings.Contains(hooks, want) {
			t.Errorf(".codex/hooks.json lacks %s:\n%s", want, hooks)
		}
	}
	agents := p.read("AGENTS.md")
	if !strings.HasPrefix(agents, "# Notes\n\nRun go test.\n\n<!-- stele:begin") || !strings.Contains(agents, "Never put credentials") {
		t.Errorf("AGENTS.md:\n%s", agents)
	}
	if p.exists(".claude") {
		t.Error("a Codex-only init made .claude/")
	}
	// Run again: nothing to write; check passes.
	if out := p.mustStele(0, "init", "--agent=codex"); !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("second init:\n%s", out)
	}
	p.mustStele(0, "check", "--all")
}

func TestCodexInitKeepsUserHooksPositions(t *testing.T) {
	p := newProject(t, codexRules)
	p.env = append(p.env, "CODEX_HOME="+t.TempDir())
	p.write(".codex/hooks.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"audit"}]}]}}`)
	p.mustStele(0, "init", "--agent=codex")
	p.write(".codex/hooks.json", strings.Replace(p.read(".codex/hooks.json"), `"PostToolUse"`, `"Stop": [{"hooks": [{"type": "command", "command": "late"}]}], "PostToolUse"`, 1))
	p.mustStele(0, "init", "--agent=codex", "--check=off")
	got := p.read(".codex/hooks.json")
	audit, steleAt := strings.Index(got, `"audit"`), strings.Index(got, `"stele hook --agent=codex --check=off"`)
	if audit < 0 || steleAt < audit || !strings.Contains(got, `"late"`) || strings.Count(got, "stele hook") != 3 {
		t.Errorf("hooks after a switch:\n%s", got)
	}
	p.mustStele(0, "uninstall", "--agent=codex")
	got = p.read(".codex/hooks.json")
	if strings.Contains(got, "stele") || !strings.Contains(got, `"audit"`) || !strings.Contains(got, `"late"`) {
		t.Errorf("hooks after uninstall:\n%s", got)
	}
}

func TestCodexDenyCheckAndRetry(t *testing.T) {
	p := codexInit(t)
	patch := "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n*** Add File: docs/a.md\n+x\n*** End Patch"
	a := p.hookRaw(p.codexEvent("PreToolUse", "turn-1", patch, "", ""))
	if !a.Deny || !strings.Contains(a.Reason, "internal/db.go, docs/a.md") || !strings.Contains(a.Reason, "plain words") {
		t.Fatalf("first patch: %+v", a)
	}
	// Codex's retry comes seconds later, after the model read the reason.
	time.Sleep(3100 * time.Millisecond)
	if a := p.hookRaw(p.codexEvent("PreToolUse", "turn-1", patch, "", "")); !a.silent() {
		t.Errorf("retry: %+v", a)
	}
	p.write("internal/db.go", "package db\n\nvar password = 1\n")
	resp := "Exit code: 0\nWall time: 0.1 seconds\nOutput:\nSuccess. Updated the following files:\nA internal/db.go\n"
	bad := "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n+\n+var password = 1\n*** End Patch"
	a = p.hookRaw(p.codexEvent("PostToolUse", "turn-1", bad, "", resp))
	if !a.Block || !strings.Contains(a.Reason, "internal/db.go:3:5:") {
		t.Errorf("post-write: %+v", a)
	}
	// The log tells the agents apart.
	if log := p.read(".stele/log.jsonl"); !strings.Contains(log, `"agent":"codex"`) || strings.Contains(log, `"agent":"claude-code"`) {
		t.Errorf("log:\n%s", log)
	}
}

func TestCodexSwitchesOff(t *testing.T) {
	p := codexInit(t, "--remind=off", "--check=off")
	if got := strings.Join(p.hookArgs, " "); got != "--agent=codex --remind=off --check=off" {
		t.Fatalf("hook args %q", got)
	}
	patch := "*** Begin Patch\n*** Add File: internal/db.go\n+var password = 1\n*** End Patch"
	if a := p.hookRaw(p.codexEvent("PreToolUse", "t", patch, "", "")); a.Deny {
		t.Errorf("--remind=off denied: %+v", a)
	}
	p.write("internal/db.go", "var password = 1\n")
	if a := p.hookRaw(p.codexEvent("PostToolUse", "t", patch, "", "")); a.Block {
		t.Errorf("--check=off blocked: %+v", a)
	}
}

func TestCodexPrimedStartupSkipsDeny(t *testing.T) {
	p := codexInit(t)
	if a := p.hookRaw(p.codexEvent("SessionStart", "", "", "", "")); !strings.Contains(a.Context, "apply_patch") {
		t.Fatalf("session start: %+v", a)
	}
	patch := "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n*** End Patch"
	if a := p.hookRaw(p.codexEvent("PreToolUse", "turn-1", patch, "", "")); !a.silent() {
		t.Errorf("a write after a primed startup: %+v", a)
	}
}

func TestCodexOverrideNotPrimed(t *testing.T) {
	p := codexInit(t)
	p.write("AGENTS.override.md", "mine\n")
	if out := p.mustStele(0, "compile"); !strings.Contains(out, "AGENTS.override.md") {
		t.Errorf("compile does not say Codex reads the override:\n%s", out)
	}
	p.hookRaw(p.codexEvent("SessionStart", "", "", "", ""))
	patch := "*** Begin Patch\n*** Add File: internal/db.go\n+package db\n*** End Patch"
	if a := p.hookRaw(p.codexEvent("PreToolUse", "turn-1", patch, "", "")); !a.Deny {
		t.Errorf("with an override, the rules were taken as loaded: %+v", a)
	}
}

func TestCodexOwnFilesRefused(t *testing.T) {
	p := codexInit(t)
	a := p.hookRaw(p.codexEvent("PreToolUse", "t", "*** Begin Patch\n*** Delete File: stele.md\n*** End Patch", "", ""))
	if !a.Deny || !strings.Contains(a.Reason, "does not let Codex change it") {
		t.Errorf("deleting stele.md: %+v", a)
	}
	a = p.hookRaw(p.codexEvent("PreToolUse", "t", "", "echo x > .codex/hooks.json", ""))
	if !a.Deny {
		t.Errorf("a shell write to .codex/hooks.json: %+v", a)
	}
}

func TestBothAgentsOneRepo(t *testing.T) {
	p := newProject(t, codexRules)
	p.env = append(p.env, "CODEX_HOME="+t.TempDir())
	out := p.mustStele(0, "init", "--agent=claude-code,codex")
	for _, want := range []string{"Claude Code hooks: remind on, check on", "Codex hooks: remind on, check on", "wrote AGENTS.md", "wrote .claude/rules/stele-rules.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("init lacks %q:\n%s", want, out)
		}
	}
	// stele init with no --agent keeps both.
	if out := p.mustStele(0, "init"); !strings.Contains(out, "Codex hooks") || !strings.Contains(out, "Claude Code hooks") {
		t.Errorf("init with no --agent:\n%s", out)
	}
	p.mustStele(0, "uninstall", "--agent=codex")
	if !p.exists(".stele") || p.exists("AGENTS.md") {
		t.Error("removing Codex took .stele/ while Claude Code still uses it, or left AGENTS.md")
	}
	p.mustStele(0, "uninstall")
	if p.exists(".stele") {
		t.Error(".stele/ left after the last agent")
	}
}
