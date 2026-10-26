package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// setVersion makes the binary report v.
func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestInitAndUninstall(t *testing.T) {
	root := project(t, "")
	setVersion(t, "v0.3.0")

	code, out, errOut := runCmd("init")
	if code != exitOK {
		t.Fatalf("init = %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"added stele hooks", "created .stele/.gitignore", "created stele.md",
		"wrote stele.lock", "wrote .claude/rules/stele-rules.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("init stdout lacks %q:\n%s", want, out)
		}
	}
	if _, out, _ = runCmd("init"); !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("init again: stdout %q", out)
	}
	if code, _, errOut := runCmd("init", "extra"); code != exitCantRun || !strings.Contains(errOut, "unexpected argument") {
		t.Errorf("init extra = %d, %q", code, errOut)
	}

	code, out, errOut = runCmd("uninstall")
	if code != exitOK || !strings.Contains(out, "removed stele hooks") || !strings.Contains(out, "removed .stele/") ||
		!strings.Contains(out, "removed .claude/rules/stele-rules.md") {
		t.Errorf("uninstall = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "stele.md")); err != nil {
		t.Errorf("uninstall removed stele.md: %v", err)
	}
}

// A stele.md that does not parse leaves init done, with a note: the rules
// file comes at the next stele compile.
func TestInitWithBrokenRules(t *testing.T) {
	root := project(t, "check: regex\n")
	code, out, errOut := runCmd("init")
	if code != exitOK || !strings.Contains(out, "added stele hooks") {
		t.Fatalf("init = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "run stele compile to write .claude/rules/stele-rules.md") {
		t.Errorf("stderr = %q, want a note to run stele compile", errOut)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "rules", "stele-rules.md")); err == nil {
		t.Error("a rules file was written from rules that do not parse")
	}
}

// A build from a commit stamps devel, so builds from two commits agree.
func TestInitFromACommitStampsDevel(t *testing.T) {
	root := project(t, "")
	setVersion(t, "v0.0.0-20261023085801-32b83aefb0cb")
	if code, _, errOut := runCmd("init"); code != exitOK {
		t.Fatalf("init = %d, stderr %q", code, errOut)
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(data), "stele hook --installed=devel") {
		t.Fatalf("settings.json = %s, %v; want the devel stamp", data, err)
	}
	setVersion(t, "v0.0.0-20261024101500-aaaaaaaaaaaa")
	if _, out, _ := runCmd("init"); !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("init from another commit: stdout %q", out)
	}
}

// Claude Code reads .claude/settings.json from the directory it starts in,
// so init sets up where it runs, never an ancestor.
func TestInitUsesTheCurrentDirectory(t *testing.T) {
	root := project(t, "")
	sub := filepath.Join(root, "sub")
	os.Mkdir(sub, 0o755)
	t.Chdir(sub)
	here, _ := os.Getwd()

	code, out, errOut := runCmd("init")
	if code != exitOK || !strings.HasPrefix(out, "stele init in "+here+"\n") {
		t.Fatalf("init = %d, stdout %q", code, out)
	}
	if !strings.Contains(errOut, "this is not the repository root ("+root) {
		t.Errorf("stderr %q, want a note naming the repository root", errOut)
	}
	if _, err := os.Stat(filepath.Join(sub, ".claude", "settings.json")); err != nil {
		t.Errorf("init did not set up the current directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error("init set up the repository root")
	}

	code, out, _ = runCmd("uninstall")
	if code != exitOK || !strings.HasPrefix(out, "stele uninstall in "+here+"\n") || !strings.Contains(out, "removed stele hooks") {
		t.Errorf("uninstall = %d, stdout %q", code, out)
	}
}

func TestInitRefusesMalformedSettings(t *testing.T) {
	root := project(t, "")
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{,}"), 0o644)
	code, _, errOut := runCmd("init")
	if code != exitCantRun || !strings.Contains(errOut, "settings.json") {
		t.Errorf("init = %d, stderr %q", code, errOut)
	}
}

// The command init registers is one stele hook runs.
func TestInitRegistersAWorkingHook(t *testing.T) {
	root := project(t, hookRules)
	setVersion(t, "v0.3.0")
	if code, _, errOut := runCmd("init"); code != exitOK {
		t.Fatalf("init: %q", errOut)
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	matchers := map[string]string{"PreToolUse": "Write|Edit|MultiEdit|Bash", "PostToolUse": "Write|Edit|MultiEdit"}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		g := s.Hooks[ev]
		if len(g) != 1 || g[0].Matcher != matchers[ev] || len(g[0].Hooks) != 1 {
			t.Fatalf("%s = %+v", ev, g)
		}
		words := strings.Fields(g[0].Hooks[0].Command)
		if words[0] != "stele" {
			t.Fatalf("%s runs %q", ev, words[0])
		}
		payload, _ := json.Marshal(write(filepath.Join(root, "a.go"), "p1", ""))
		payload = bytes.Replace(payload, []byte("PreToolUse"), []byte(ev), 1)
		old := stdin
		stdin = bytes.NewReader(payload)
		code, out, _ := runCmd(words[1:]...)
		stdin = old
		if code != exitOK || (ev == "PreToolUse" && !strings.Contains(out, "deny")) {
			t.Errorf("%s: exit %d, stdout %q", ev, code, out)
		}
	}
}

// hookWith runs stele hook with args on a PreToolUse write to file.
func hookWith(t *testing.T, file, prompt string, args ...string) hookOutput {
	t.Helper()
	data, _ := json.Marshal(write(file, prompt, ""))
	old := stdin
	stdin = bytes.NewReader(data)
	defer func() { stdin = old }()
	code, out, _ := runCmd(append([]string{"hook"}, args...)...)
	if code != exitOK {
		t.Fatalf("hook exit = %d", code)
	}
	var o hookOutput
	if out != "" {
		if err := json.Unmarshal([]byte(out), &o); err != nil {
			t.Fatalf("hook stdout is not JSON: %q", out)
		}
	}
	return o
}

func TestHookWarnsOfAnotherVersion(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	file := filepath.Join(root, "a.go")
	setVersion(t, "v0.0.2")

	o := hookWith(t, file, "p1", "--installed=v0.0.1")
	if !strings.Contains(o.SystemMessage, "installed by stele v0.0.1; this is stele v0.0.2") {
		t.Errorf("first write: warning %q", o.SystemMessage)
	}
	if o.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("the warning replaced the rules")
	}
	if o := hookWith(t, file, "p2", "--installed=v0.0.1"); o.SystemMessage != "" {
		t.Errorf("second write: warning %q, want it once a session", o.SystemMessage)
	}
	if !strings.Contains(o.SystemMessage, "Run stele init") {
		t.Errorf("a newer binary: warning %q, want it sent to stele init", o.SystemMessage)
	}

	// An older binary must not run init: its init would take flagged hooks
	// for hand-written ones and add a second set (ADR-0006).
	for _, tt := range []struct {
		name, binary string
		args         []string
	}{
		{"older release", "v0.0.1", []string{"--installed=v0.0.2"}},
		{"pre-release of the stamp", "v1.0.0-dev.2", []string{"--installed=v1.0.0"}},
		{"older, with a switch", "v0.0.1", []string{"--installed=v0.0.2", "--remind=off"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, hookRules)
			setVersion(t, tt.binary)
			o := hookWith(t, filepath.Join(root, "a.go"), "p1", tt.args...)
			if !strings.Contains(o.SystemMessage, "Upgrade stele") || strings.Contains(o.SystemMessage, "Run stele init") {
				t.Errorf("warning %q, want it told to upgrade, not to run init", o.SystemMessage)
			}
			if slices.Contains(tt.args, "--remind=off") && o.HookSpecificOutput.PermissionDecision == "deny" {
				t.Error("remind off, yet the write was denied")
			}
		})
	}

	for _, tt := range []struct{ name, binary, installed string }{
		{"same version", "v0.0.2", "--installed=v0.0.2"},
		{"no stamp", "v0.0.2", ""},
		{"devel binary", "devel", "--installed=v0.0.1"},
		{"pseudo binary", "v0.0.0-20261024000000-aaaaaaaaaaaa", "--installed=v0.0.1"},
		{"dirty binary", "v0.0.2+dirty", "--installed=v0.0.1"},
		{"pseudo stamp", "v0.0.2", "--installed=v0.0.0-20261023085801-32b83aefb0cb"},
		{"devel stamp", "v0.0.2", "--installed=devel"},
		{"two pseudo-versions", "v0.0.0-20261024000000-aaaaaaaaaaaa", "--installed=v0.0.0-20261023085801-32b83aefb0cb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, hookRules)
			setVersion(t, tt.binary)
			var args []string
			if tt.installed != "" {
				args = append(args, tt.installed)
			}
			if o := hookWith(t, filepath.Join(root, "a.go"), "p1", args...); o.SystemMessage != "" {
				t.Errorf("warning %q, want none", o.SystemMessage)
			}
		})
	}

	t.Run("not a stele project", func(t *testing.T) {
		setVersion(t, "v0.0.2")
		dir := t.TempDir()
		if o := hookWith(t, filepath.Join(dir, "a.go"), "p1", "--installed=v0.0.1"); o.SystemMessage != "" {
			t.Errorf("warning %q outside a stele project", o.SystemMessage)
		}
	})
}

func TestBinaryVersion(t *testing.T) {
	setVersion(t, "v9.9.9")
	if got := binaryVersion(); got != "v9.9.9" {
		t.Errorf("binaryVersion = %q", got)
	}
	setVersion(t, "")
	if got := binaryVersion(); got == "" {
		t.Error("binaryVersion is empty")
	}
}
