package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/adapter"
)

// hooksOf reads the commands of each event's groups, in order.
func hooksOf(t *testing.T, data []byte) map[string][][]string {
	t.Helper()
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(data), "\xef\xbb\xbf")), &s); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	out := map[string][][]string{}
	for ev, gs := range s.Hooks {
		for _, g := range gs {
			var cmds []string
			for _, h := range g.Hooks {
				cmds = append(cmds, h.Command)
			}
			out[ev] = append(out[ev], append([]string{"matcher=" + g.Matcher}, cmds...))
		}
	}
	return out
}

const userHooks = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "audit.sh"}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]
  },
  "other": 1.50
}
`

func TestInstallAppendsAfterUserGroups(t *testing.T) {
	out, changed, notes, err := Install([]byte(userHooks), adapter.Switches{})
	if err != nil || !changed {
		t.Fatalf("Install: %v, changed %v", err, changed)
	}
	got := hooksOf(t, out)
	pre := got["PreToolUse"]
	if len(pre) != 2 || pre[0][1] != "audit.sh" || pre[1][1] != "stele hook --agent=codex" || pre[1][0] != "matcher=^(apply_patch|Bash)$" {
		t.Errorf("PreToolUse = %v: the user's group first, stele's after", pre)
	}
	if got["PostToolUse"][0][1] != "stele hook --agent=codex" || got["SessionStart"][0][1] != "stele hook --agent=codex" {
		t.Errorf("hooks = %v", got)
	}
	if got["Stop"][0][1] != "notify" || !strings.Contains(string(out), "1.50") {
		t.Errorf("the user's other settings changed:\n%s", out)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "/hooks") {
		t.Errorf("notes %q do not say to approve the hooks", notes)
	}
	if sw, ok := Installed(out); !ok || sw != (adapter.Switches{}) {
		t.Errorf("Installed = %v, %v", sw, ok)
	}
}

func TestInstallIdempotent(t *testing.T) {
	out, _, _, _ := Install(nil, adapter.Switches{})
	again, changed, notes, err := Install(out, adapter.Switches{})
	if err != nil || changed || string(again) != string(out) || len(notes) != 0 {
		t.Errorf("second Install: changed %v, notes %v, err %v", changed, notes, err)
	}
}

// A switch rewrites stele's handler where it is: Codex keys its approvals by
// a hook's place, so the user's hooks around it keep theirs.
func TestInstallSwitchInPlace(t *testing.T) {
	first, _, _, _ := Install([]byte(userHooks), adapter.Switches{})
	// The user adds a hook after stele's.
	var s map[string]any
	json.Unmarshal(first, &s)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	s["hooks"].(map[string]any)["PreToolUse"] = append(pre, map[string]any{"matcher": "x", "hooks": []any{map[string]any{"type": "command", "command": "late.sh"}}})
	withLate, _ := json.Marshal(s)

	out, changed, notes, err := Install(withLate, adapter.Switches{CheckOff: true})
	if err != nil || !changed {
		t.Fatalf("Install: %v, changed %v", err, changed)
	}
	got := hooksOf(t, out)["PreToolUse"]
	want := [][]string{{"matcher=Bash", "audit.sh"}, {"matcher=^(apply_patch|Bash)$", "stele hook --agent=codex --check=off"}, {"matcher=x", "late.sh"}}
	if len(got) != 3 || strings.Join(got[0], "|") != strings.Join(want[0], "|") || strings.Join(got[1], "|") != strings.Join(want[1], "|") || strings.Join(got[2], "|") != strings.Join(want[2], "|") {
		t.Errorf("PreToolUse = %v, want %v", got, want)
	}
	if sw, _ := Installed(out); !sw.CheckOff || sw.RemindOff {
		t.Errorf("switches = %+v", sw)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "asks again") {
		t.Errorf("notes %q", notes)
	}
}

func TestInstallKeepsBOMAndCRLF(t *testing.T) {
	in := "\xef\xbb\xbf" + strings.ReplaceAll(userHooks, "\n", "\r\n")
	out, _, _, err := Install([]byte(in), adapter.Switches{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "\xef\xbb\xbf") || strings.Count(string(out), "\r\n") != strings.Count(string(out), "\n") {
		t.Errorf("layout lost:\n%q", out)
	}
}

func TestInstallRefusesMalformed(t *testing.T) {
	for _, in := range []string{"{", `{"hooks": {"PreToolUse": {}}}`, `[1]`} {
		if _, _, _, err := Install([]byte(in), adapter.Switches{}); err == nil {
			t.Errorf("Install(%q) succeeded", in)
		}
	}
}

func TestInstallNotesHandWrittenHook(t *testing.T) {
	in := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"stele hook"}]}]}}`
	out, _, notes, err := Install([]byte(in), adapter.Switches{})
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "did not write") {
		t.Fatalf("notes %q, err %v", notes, err)
	}
	if hooksOf(t, out)["PreToolUse"][0][1] != "stele hook" {
		t.Error("the hand-written hook was not left alone")
	}
}

func TestUninstallOnlyStele(t *testing.T) {
	installed, _, _, _ := Install([]byte(userHooks), adapter.Switches{RemindOff: true})
	out, removed, empty, err := Uninstall(installed)
	if err != nil || removed != 3 || empty {
		t.Fatalf("Uninstall: removed %d, empty %v, err %v", removed, empty, err)
	}
	got := hooksOf(t, out)
	if len(got["PreToolUse"]) != 1 || got["PreToolUse"][0][1] != "audit.sh" || got["PostToolUse"] != nil || got["SessionStart"] != nil {
		t.Errorf("after Uninstall: %v", got)
	}
	alone, _, _, _ := Install(nil, adapter.Switches{})
	if _, _, empty, _ := Uninstall(alone); !empty {
		t.Error("nothing left, yet not empty")
	}
	if out, removed, _, _ := Uninstall([]byte(userHooks)); removed != 0 || string(out) != userHooks {
		t.Error("nothing to remove, yet the file changed")
	}
}

// Uninstall keeps the places of the user's hooks after stele's, which their
// approvals are keyed by: stele's group stays, empty; install fills it again.
func TestUninstallKeepsPlaces(t *testing.T) {
	installed, _, _, _ := Install([]byte(userHooks), adapter.Switches{})
	var s map[string]any
	json.Unmarshal(installed, &s)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	s["hooks"].(map[string]any)["PreToolUse"] = append(pre, map[string]any{"matcher": "x", "hooks": []any{map[string]any{"type": "command", "command": "late.sh"}}})
	withLate, _ := json.Marshal(s)

	out, removed, _, err := Uninstall(withLate)
	if err != nil || removed != 3 {
		t.Fatalf("Uninstall: %d, %v", removed, err)
	}
	got := hooksOf(t, out)["PreToolUse"]
	if len(got) != 3 || len(got[1]) != 1 || got[1][0] != "matcher=^(apply_patch|Bash)$" || got[2][1] != "late.sh" {
		t.Errorf("PreToolUse after uninstall = %v: late.sh must stay third", got)
	}
	if hooksOf(t, out)["PostToolUse"] != nil {
		t.Errorf("an empty group at the end stayed: %s", out)
	}
	again, _, _, _ := Install(out, adapter.Switches{})
	got = hooksOf(t, again)["PreToolUse"]
	if len(got) != 3 || got[1][1] != "stele hook --agent=codex" || got[2][1] != "late.sh" {
		t.Errorf("PreToolUse after a new install = %v", got)
	}
}

func TestOtherSteleHook(t *testing.T) {
	ours, _, _, _ := Install(nil, adapter.Switches{})
	if OtherSteleHook(ours, adapter.Switches{}) {
		t.Error("init's own hooks count as another")
	}
	if !OtherSteleHook(ours, adapter.Switches{CheckOff: true}) {
		t.Error("hooks for other switches do not count")
	}
	toml := "[[hooks.PreToolUse]]\nmatcher = \".*\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"stele hook\"\n"
	if !OtherSteleHook([]byte(toml), adapter.Switches{}) || OtherSteleHook([]byte("model = \"x\"\n"), adapter.Switches{}) {
		t.Error("config.toml's stele hook not seen, or one seen where there is none")
	}
}

func TestHookArgs(t *testing.T) {
	v, sw := Adapter{}.HookArgs([]string{"--agent=codex", "--remind=off", "--check=off", "--check=on"})
	if v != "" || !sw.RemindOff || sw.CheckOff {
		t.Errorf("HookArgs = %q, %+v", v, sw)
	}
}
