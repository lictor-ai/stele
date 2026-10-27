package claudecode

import (
	"os"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/adapter"
)

// Adapter is Claude Code behind the adapter interface (ADR-0009). Its
// methods call the package's functions, which tests use directly.
type Adapter struct{}

var _ adapter.Adapter = Adapter{}

// eventNames are the hook events each kind answers.
var eventNames = map[adapter.Kind]string{
	adapter.PreTool:       "PreToolUse",
	adapter.PreShell:      "PreToolUse",
	adapter.PostTool:      "PostToolUse",
	adapter.SessionStart:  "SessionStart",
	adapter.SubagentStart: "SubagentStart",
}

func (Adapter) Parse(stdin []byte) (adapter.Event, error) { return Parse(stdin) }

func (Adapter) Render(ev adapter.Event, a adapter.Answer) []byte {
	return Render(eventNames[ev.Kind], a)
}

func (Adapter) HookArgs(args []string) (string, adapter.Switches) {
	return InstalledVersion(args), HookSwitches(args)
}

func (Adapter) Loaded(ev adapter.Event, root string) (string, bool, bool) { return Loaded(ev, root) }

func (Adapter) SettingsFile() string { return SettingsFile }
func (Adapter) Hooks() []string      { return Events() }

func (Adapter) Install(settings []byte, version string, sw adapter.Switches) ([]byte, bool, []string, error) {
	return Install(settings, version, sw)
}

func (Adapter) Uninstall(settings []byte) ([]byte, int, bool, error) { return Uninstall(settings) }

func (Adapter) Installed(settings []byte) (string, adapter.Switches, bool) {
	return Installed(settings)
}

func (Adapter) OtherSteleHook(settings []byte, version string, sw adapter.Switches) bool {
	return OtherSteleHook(settings, version, sw)
}

// Settings are the project settings Claude Code loads, which load stele's
// hooks, and which it reloads mid-session (#56).
func (Adapter) Settings() []string {
	return []string{filepath.ToSlash(SettingsFile), ".claude/settings.local.json"}
}

// UserSettings is the user's settings.json, in CLAUDE_CONFIG_DIR or else
// ~/.claude. A relative CLAUDE_CONFIG_DIR is the process's.
func (Adapter) UserSettings() []string {
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		cfg = filepath.Join(home, ".claude")
	}
	cfg, err := filepath.Abs(cfg)
	if err != nil {
		return nil
	}
	return []string{filepath.Join(cfg, "settings.json")}
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
func (Adapter) SessionContext() string                  { return SessionContext() }
