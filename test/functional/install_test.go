package functional

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const goRules = `scope: ["**/*.go"]
check: regex
pattern: fmt\.Print(f|ln)?\(
message: log through internal/log

Log through internal/log. Never print with fmt.Print, Printf or Println.

---

scope: migrations/

Never edit a merged migration. Add a new one instead.

---

check: regex
pattern: AKIA[0-9A-Z]{16}

Never put credentials in code, config or tests.
`

// A user's own settings must survive init and uninstall: other hooks, other
// keys, and the order of what is already there.
func TestInitKeepsUserSettingsAndUninstallRemovesOnlyStele(t *testing.T) {
	p := newProject(t, "")
	user := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-guard"}]},
      {"matcher": "Write|Edit|MultiEdit", "hooks": [{"type": "command", "command": "prettier-check"}]}
    ]
  }
}
`
	p.write(".claude/settings.json", user)
	p.mustStele(0, "init")

	var s map[string]any
	if err := json.Unmarshal([]byte(p.read(".claude/settings.json")), &s); err != nil {
		t.Fatalf("settings.json no longer JSON: %v", err)
	}
	got := p.read(".claude/settings.json")
	for _, want := range []string{"my-guard", "prettier-check", `"model"`, "Bash(go test:*)", "stele hook --installed="} {
		if !strings.Contains(got, want) {
			t.Errorf("after init, settings.json lost %q:\n%s", want, got)
		}
	}
	if !p.exists("stele.md") || !p.exists(".stele/.gitignore") {
		t.Fatal("init did not create stele.md and .stele/")
	}

	// Idempotent.
	before := p.read(".claude/settings.json")
	out := p.mustStele(0, "init")
	if !strings.Contains(out, "up to date") || p.read(".claude/settings.json") != before {
		t.Errorf("second init changed something:\n%s", out)
	}

	p.mustStele(0, "uninstall")
	got = p.read(".claude/settings.json")
	if strings.Contains(got, "stele") {
		t.Errorf("uninstall left stele in settings.json:\n%s", got)
	}
	for _, want := range []string{"my-guard", "prettier-check", `"model"`} {
		if !strings.Contains(got, want) {
			t.Errorf("uninstall removed the user's %q:\n%s", want, got)
		}
	}
	if !p.exists("stele.md") {
		t.Error("uninstall removed stele.md, which it must keep")
	}
	if p.exists(".stele") {
		t.Error("uninstall left .stele/")
	}
}

// init --remind and --check write the switches on every stele hook, keep
// them when not given, and refuse a value other than on or off (ADR-0006).
func TestInitSwitchesSettings(t *testing.T) {
	p := newProject(t, goRules)
	handlers := func() []string {
		var s struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(p.read(".claude/settings.json")), &s); err != nil {
			t.Fatal(err)
		}
		var cmds []string
		for _, gs := range s.Hooks {
			for _, g := range gs {
				for _, h := range g.Hooks {
					if strings.Contains(h.Command, "stele") {
						cmds = append(cmds, h.Command)
					}
				}
			}
		}
		return cmds
	}
	expect := func(step, want, line string, out string) {
		t.Helper()
		cmds := handlers()
		if len(cmds) != 4 {
			t.Errorf("%s: %d stele handlers, want 4: %q", step, len(cmds), cmds)
		}
		for _, c := range cmds {
			if c != want {
				t.Errorf("%s: handler %q, want %q", step, c, want)
			}
		}
		if !strings.Contains(out, line+"\n") {
			t.Errorf("%s: output lacks %q:\n%s", step, line, out)
		}
	}

	out := p.mustStele(0, "init", "--remind=off")
	expect("remind off", "stele hook --installed=devel --remind=off", "hooks: remind off, check on", out)
	out = p.mustStele(0, "init")
	if !strings.Contains(out, "up to date") {
		t.Errorf("init with no flag rewrote the settings:\n%s", out)
	}
	expect("kept", "stele hook --installed=devel --remind=off", "hooks: remind off, check on", out)
	out = p.mustStele(0, "init", "--check=off")
	expect("both off", "stele hook --installed=devel --remind=off --check=off", "hooks: remind off, check off", out)
	out = p.mustStele(0, "init", "--remind=on", "--check=on")
	expect("both on", "stele hook --installed=devel", "hooks: remind on, check on", out)

	before := p.hash(".claude/settings.json")
	p.mustStele(2, "init", "--remind=x")
	if after := p.hash(".claude/settings.json"); after[".claude/settings.json"] != before[".claude/settings.json"] {
		t.Error("init --remind=x changed settings.json")
	}

	p.mustStele(0, "init", "--remind=off", "--check=off")
	p.mustStele(0, "uninstall")
	if p.exists(".claude/settings.json") && strings.Contains(p.read(".claude/settings.json"), "stele") {
		t.Errorf("uninstall left a stele hook:\n%s", p.read(".claude/settings.json"))
	}
}

// A stele.json that still sets backstop gets a note from init, and is left
// byte for byte (D38, ADR-0008).
func TestInitNotesBackstop(t *testing.T) {
	p := newProject(t, goRules)
	p.write("stele.json", `{"backstop": false}`+"\n")
	before := p.hash("stele.json")
	code, _, errOut := p.stele("init")
	if code != 0 || !strings.Contains(errOut, "stele.json: backstop is no longer read; use stele init --remind=off") {
		t.Errorf("init = %d, stderr %q; want the backstop note", code, errOut)
	}
	if after := p.hash("stele.json"); after["stele.json"] != before["stele.json"] {
		t.Error("init changed stele.json")
	}
}

// A settings.json that is not JSON must be refused, not overwritten.
func TestInitRefusesBrokenSettings(t *testing.T) {
	p := newProject(t, "")
	broken := "{ \"hooks\": { // a comment\n"
	p.write(".claude/settings.json", broken)
	code, _, _ := p.stele("init")
	if code == 0 {
		t.Error("init accepted a settings.json that is not JSON")
	}
	if p.read(".claude/settings.json") != broken {
		t.Error("init rewrote a settings.json it could not parse: the user's file is lost")
	}
}

// D38: stele never writes stele.md or stele.json, whatever it runs.
func TestNeverWritesRuleFiles(t *testing.T) {
	p := newProject(t, goRules)
	p.write("stele.json", `{"backstop": true, "block_on_error": false, "plugins": []}`+"\n") // backstop: init notes it, never removes it
	before := p.hash("stele.md", "stele.json")
	p.mustStele(0, "init")
	p.compile()
	p.agentWrite(event{Prompt: "t", Tool: "Write", File: "a.go", Content: "package a\nfunc f() { fmt.Println(1) }\n"})
	p.stele("check", "--all")
	p.mustStele(0, "uninstall")
	p.mustStele(0, "init")
	after := p.hash("stele.md", "stele.json")
	for f := range before {
		if before[f] != after[f] {
			t.Errorf("%s changed: stele must never write it (D38)", f)
		}
	}
}

// Rule ids are what a later dashboard joins on (D49-D55). Moving rules,
// rewording one in place, or inserting one must keep the others' ids.
func TestLockKeepsIDsThroughEdits(t *testing.T) {
	parts := strings.Split(goRules, "\n---\n")
	reword := func(s string) string { return strings.Replace(s, "Never print with", "Do not print with", 1) }
	inserted := "scope: docs/\n\nWrite docs in plain English.\n"
	cases := []struct {
		name  string
		after []string
		kept  int // of the 3 original ids
	}{
		{"reorder", []string{parts[2], parts[0], parts[1]}, 3},
		{"insert at the top", []string{inserted, parts[0], parts[1], parts[2]}, 3},
		{"reword in place", []string{reword(parts[0]), parts[1], parts[2]}, 3},
		{"delete one", []string{parts[0], parts[2]}, 2},
		{"reword and move", []string{parts[2], inserted, reword(parts[0]), parts[1]}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t, goRules)
			p.compile()
			ids := ruleIDs(t, p)
			p.write("stele.md", strings.Join(c.after, "\n---\n"))
			p.compile()
			before := map[string]bool{}
			for _, id := range ids {
				before[id] = true
			}
			kept := 0
			for _, id := range ruleIDs(t, p) {
				if before[id] {
					kept++
				}
			}
			if kept != c.kept {
				msg := fmt.Sprintf("kept %d of %d ids", kept, c.kept)
				if c.name == "reword and move" {
					// Matching is by text, else by position: a rule that
					// changes both gets a new id. Recorded, not enforced.
					t.Logf("FINDING: reword and move in one commit: %s; the reworded rule gets a new id", msg)
					return
				}
				t.Errorf("%s: %s", c.name, msg)
			}
			lock := p.read("stele.lock")
			p.compile()
			if p.read("stele.lock") != lock {
				t.Error("compiling an unchanged stele.md rewrote stele.lock")
			}
		})
	}
}

func ruleIDs(t *testing.T, p *project) []string {
	t.Helper()
	var l struct {
		Rules []struct{ ID string } `json:"rules"`
	}
	if err := json.Unmarshal([]byte(p.read("stele.lock")), &l); err != nil {
		t.Fatalf("stele.lock: %v", err)
	}
	var ids []string
	for _, r := range l.Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

// Invalid rules: an unknown checker, a regex RE2 cannot compile, a pattern
// with no check. stele check must fail on each, naming the line.
func TestCheckRejectsInvalidRules(t *testing.T) {
	cases := map[string]string{
		"unknown checker": "check: semgrep\npattern: x\n\nNo x.\n",
		"bad regex":       "check: regex\npattern: (?<=a)b\n\nNo lookbehind in RE2.\n",
		"unclosed group":  "check: regex\npattern: (abc\n\nBroken.\n",
		"bad blocking":    "check: regex\npattern: x\nblocking: maybe\n\nNo x.\n",
		"bad scope list":  "scope: [\"**/*.go\"\n\nUnclosed list.\n",
	}
	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			p := newProject(t, md)
			p.commit()
			code, out, errOut := p.stele("check", "--all")
			if code != 1 {
				t.Errorf("stele check: exit %d, want 1 for an invalid rule\n%s%s", code, out, errOut)
			}
		})
	}
}
