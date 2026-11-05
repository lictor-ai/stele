package install

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lictor-ai/stele/internal/adapter"
)

// otherAgent is a second agent: Claude Code's hooks in another settings
// file, so two agents can be set up in one root.
type otherAgent struct{ adapter.Adapter }

func (otherAgent) SettingsFile() string { return filepath.Join(".other", "settings.json") }
func (otherAgent) Settings() []string   { return []string{".other/settings.json"} }

// .stele/ stays while another agent's hooks use it, and goes with the last.
func TestUninstallKeepsSteleForOtherAgents(t *testing.T) {
	root := t.TempDir()
	other := otherAgent{cc}
	for _, a := range []adapter.Adapter{cc, other} {
		if _, _, err := Init(a, root, "v1.0.0", Set{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Uninstall(cc, root, []adapter.Adapter{other}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); err != nil {
		t.Fatalf(".stele/ went while the other agent's hooks use it: %v", err)
	}
	if _, _, err := Uninstall(other, root, []adapter.Adapter{cc}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); err == nil {
		t.Error(".stele/ left after the last agent's hooks went")
	}
}
