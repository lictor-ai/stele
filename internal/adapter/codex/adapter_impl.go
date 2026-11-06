package codex

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/safefile"
)

// Adapter is Codex behind the adapter interface (ADR-0009, ADR-0012). Its
// methods call the package's functions, which tests use directly.
type Adapter struct{}

var _ adapter.Adapter = Adapter{}

func (Adapter) Name() string { return "codex" }

func (Adapter) Parse(stdin []byte) (adapter.Event, error) { return Parse(stdin) }

func (Adapter) Render(ev adapter.Event, a adapter.Answer) []byte {
	return Render(eventNames[ev.Kind], a)
}

// HookArgs: Codex's hooks carry no version (ADR-0012).
func (Adapter) HookArgs(args []string) (string, adapter.Switches) { return "", HookSwitches(args) }

func (Adapter) Loaded(ev adapter.Event, root string) adapter.Load { return Loaded(ev, root) }

func (Adapter) SettingsFile() string { return SettingsFile }
func (Adapter) Hooks() []string      { return append([]string(nil), events...) }

// Install ignores the version: the command carries none.
func (Adapter) Install(settings []byte, _ string, sw adapter.Switches) ([]byte, bool, []string, error) {
	return Install(settings, sw)
}

func (Adapter) Uninstall(settings []byte) ([]byte, int, bool, error) { return Uninstall(settings) }

func (Adapter) Installed(settings []byte) (string, adapter.Switches, bool) {
	sw, ok := Installed(settings)
	return "", sw, ok
}

func (Adapter) OtherSteleHook(settings []byte, _ string, sw adapter.Switches) bool {
	return OtherSteleHook(settings, sw)
}

// Settings are the project files that configure Codex: its hooks file, and
// its config.toml, which can hold hooks too.
func (Adapter) Settings() []string {
	return []string{filepath.ToSlash(SettingsFile), configFile}
}

// UserSettings are the user's Codex config and hooks, in CODEX_HOME or else
// ~/.codex.
func (Adapter) UserSettings() []string {
	home := codexHome()
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, "config.toml"), filepath.Join(home, "hooks.json")}
}

func (Adapter) RulesFile() string                       { return RulesFile }
func (Adapter) MaxRulesFile() int64                     { return MaxRulesFile }
func (Adapter) MaxRulesChars() int                      { return MaxRulesChars }
func (Adapter) RenderRules(rules []adapter.Rule) []byte { return RenderRules(rules) }
func (Adapter) RuleHash(r adapter.Rule) string          { return RuleHash(r) }
func (Adapter) ReadRules(data []byte) map[string]string { return ReadRules(data) }
func (Adapter) OwnedRules(data []byte) bool             { return OwnedRules(data) }
func (Adapter) SameRules(disk, want []byte) bool        { return SameRules(disk, want) }
func (Adapter) Words() adapter.Words                    { return words }
func (Adapter) SessionContext() string                  { return sessionText() }

// AGENTS.md is the user's: stele writes its section only.
func (Adapter) SharedRulesFile() bool { return true }

func (Adapter) SpliceRules(disk, rendered []byte) ([]byte, bool) { return SpliceRules(disk, rendered) }

func (Adapter) UnspliceRules(disk []byte) ([]byte, bool, bool) { return UnspliceRules(disk) }

// RulesNotes says when Codex will start without the rules: an override file
// in root, which it reads instead.
func (Adapter) RulesNotes(root string) []string {
	var notes []string
	if _, err := os.Lstat(filepath.Join(root, overrideFile)); err == nil {
		notes = append(notes, overrideFile+" is here: Codex reads it instead of "+RulesFile+
			", so its sessions start without stele's rules; the deny before a write still shows them")
	}
	if data, err := safefile.ReadRegular(filepath.Join(root, RulesFile), MaxRulesFile); err == nil && utf16(data) {
		notes = append(notes, RulesFile+" is UTF-16: stele writes only into UTF-8 text; save it as UTF-8, then run stele compile")
	}
	return notes
}

func (Adapter) VersionProbe() []string { return append([]string(nil), versionProbe...) }

func (Adapter) VersionNotes(out []byte, err error) []string {
	return VersionNotes(out, err, runtime.GOOS)
}
