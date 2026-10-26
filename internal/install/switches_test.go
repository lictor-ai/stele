package install

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
)

func boolp(b bool) *bool { return &b }

func TestInitSetsSwitches(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".claude", "settings.json")
	check := func(name string, want claudecode.Switches, wantFlags string) {
		t.Helper()
		got := read(t, settings)
		if n := strings.Count(got, `"stele hook --installed=v1.2.0`+wantFlags+`"`); n != len(claudecode.Events()) {
			t.Errorf("%s: %d handlers are %q, want all:\n%s", name, n, "stele hook --installed=v1.2.0"+wantFlags, got)
		}
		if sw, ok := InstalledSwitches(root); !ok || sw != want {
			t.Errorf("%s: InstalledSwitches = %+v, %v; want %+v", name, sw, ok, want)
		}
	}
	if _, _, err := Init(root, "v1.2.0", Set{Remind: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	check("remind off", claudecode.Switches{RemindOff: true}, " --remind=off")

	changes, _, err := Init(root, "v1.2.0", Set{})
	if err != nil || len(changes) != 0 {
		t.Errorf("Init with no switch = %q, %v; want no changes", changes, err)
	}
	check("kept", claudecode.Switches{RemindOff: true}, " --remind=off")

	changes, _, err = Init(root, "v1.2.0", Set{Check: boolp(false)})
	if err != nil || len(changes) != 1 || changes[0] != "updated stele hooks in "+claudecode.SettingsFile {
		t.Errorf("Init check off = %q, %v", changes, err)
	}
	check("both off", claudecode.Switches{RemindOff: true, CheckOff: true}, " --remind=off --check=off")

	if _, _, err := Init(root, "v1.2.0", Set{Remind: boolp(true), Check: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	check("both on", claudecode.Switches{}, "")
}

func TestInitKeepsSwitchesAcrossUpgrade(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Init(root, "v1.2.0", Set{Remind: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Init(root, "v1.3.0", Set{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(root, ".claude", "settings.json"))
	if strings.Count(got, `"stele hook --installed=v1.3.0 --remind=off"`) != len(claudecode.Events()) {
		t.Errorf("settings.json after the upgrade:\n%s", got)
	}
}

func TestInitFromACommitKeepsSwitches(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Init(root, "v1.2.0", Set{Check: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Init(root, pseudoA, Set{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(root, ".claude", "settings.json"))
	if strings.Count(got, `"stele hook --installed=v1.2.0 --check=off"`) != len(claudecode.Events()) {
		t.Errorf("settings.json after init from a commit:\n%s", got)
	}
}

func TestInitNotesBackstop(t *testing.T) {
	root := t.TempDir()
	const policy = `{"backstop": false, "block_on_error": true}`
	write(t, filepath.Join(root, "stele.json"), policy)
	_, notes, err := Init(root, "v1.2.0", Set{})
	if err != nil || !slices.Contains(notes, BackstopNote) {
		t.Errorf("Init = %q, %v; want the backstop note", notes, err)
	}
	if got := read(t, filepath.Join(root, "stele.json")); got != policy {
		t.Errorf("stele.json changed: %q", got)
	}

	for _, content := range []string{"", `{"block_on_error": true}`} {
		root := t.TempDir()
		if content != "" {
			write(t, filepath.Join(root, "stele.json"), content)
		}
		if _, notes, _ := Init(root, "v1.2.0", Set{}); slices.Contains(notes, BackstopNote) {
			t.Errorf("Init with stele.json %q notes backstop", content)
		}
	}
}
