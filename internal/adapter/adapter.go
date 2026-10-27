package adapter

// Kind is what a hook call is about, in no agent's words.
type Kind int

const (
	// Other is a call stele does not handle.
	Other Kind = iota
	// PreTool: a tool is about to run on File. Writes says whether it writes.
	PreTool
	// PostTool: a tool has run on File.
	PostTool
	// PreShell: a shell command is about to run: Command.
	PreShell
	// SessionStart: a session starts, resumes, is compacted or cleared.
	SessionStart
	// SubagentStart: a subagent starts.
	SubagentStart
)

// Starts reports whether k starts a session or a subagent: a call that
// names no file.
func (k Kind) Starts() bool { return k == SessionStart || k == SubagentStart }

// Event is one hook call, as the agent-neutral flow needs it.
type Event struct {
	Kind      Kind
	SessionID string
	PromptID  string // the user turn; empty before the first prompt
	AgentID   string // empty for the main agent
	Cwd       string // the directory the agent runs in
	// Writes is whether the tool of a PreTool or PostTool call writes File;
	// a read does not.
	Writes bool
	// Whole is whether the tool replaced the whole file, so every line of it
	// counts as written.
	Whole bool
	// File is the absolute path of the file the tool touches, or "" when the
	// tool names no file stele knows how to find.
	File string
	// Command is a shell command line, which stele reads for the files it
	// writes (#57), never runs; Background is whether it runs in the
	// background.
	Command    string
	Background bool
	// NewText is what the tool puts in the file: the whole content, or the
	// new text of each edit.
	NewText []string
	// ReplaceAll is, for each NewText of an edit, whether it replaces every
	// match.
	ReplaceAll []bool
	// Added is the lines of the file as written that the tool added, from
	// the agent's own patch. Patched is false when there is no patch stele
	// can trust; Added then means nothing.
	Added   []Lines
	Patched bool
	// Native is the adapter's own reading of the call, read only by its
	// methods.
	Native any
}

// Lines is lines First to Last of a file, 1-based and inclusive.
type Lines struct{ First, Last int }

// Answer is what stele tells the agent.
type Answer struct {
	// Deny stops a PreTool or PreShell call. After a PostTool call it sends
	// Reason back as a block: the write is not undone. Reason is what the
	// agent reads.
	Deny   bool
	Reason string
	// Ask, when set, makes the agent ask the user before a PreTool or
	// PreShell call, showing Ask in the prompt. Deny wins over it. It is
	// ignored after a write.
	Ask string
	// Context is added to the agent's context alongside the tool result.
	Context string
	// Warning is shown to the user, not to the agent.
	Warning string
}

// Switches are the parts of the write hooks a project turned off with stele
// init (ADR-0006, ADR-0008). The zero value is both on, the default.
type Switches struct {
	// RemindOff: before a write, never deny it to show the rules.
	RemindOff bool
	// CheckOff: after a write, never check it.
	CheckOff bool
}

// String is how init shows the switches: "remind on, check off".
func (s Switches) String() string {
	state := func(off bool) string {
		if off {
			return "off"
		}
		return "on"
	}
	return "remind " + state(s.RemindOff) + ", check " + state(s.CheckOff)
}

// Rule is one rule as the agent's rules file shows it.
type Rule struct {
	ID    string
	Scope []string
	Body  string
}

// Words are how stele's messages name the agent and its tools.
type Words struct {
	Agent string // the agent's name: "Claude Code"
	Shell string // its shell tool: "Bash"
	// WriteTools completes "Write files with … only": "Write and Edit".
	WriteTools string
	// WriteInstead completes "Write it with …:": "the Write or Edit tool
	// instead (MultiEdit too)".
	WriteInstead string
}

// Adapter is everything stele knows about one agent (ADR-0009).
type Adapter interface {
	// Parse reads a hook payload. A payload that is not the agent's is an
	// error.
	Parse(stdin []byte) (Event, error)
	// Render returns the stdout for an answer to ev, or nil when there is
	// nothing to say. The exit code is always 0.
	Render(ev Event, a Answer) []byte
	// HookArgs reads stele hook's own arguments: the version stamped on the
	// installed hook, "" when there is none, and the switches.
	HookArgs(args []string) (version string, sw Switches)

	// Loaded says, for a start event and the root found from its Cwd,
	// whether the agent starting now loaded root's rules file (ok), as which
	// agent (its id, "" for the main agent), and whether this start launches
	// or resumes the session, so root becomes its launch root.
	Loaded(ev Event, root string) (agent string, launch, ok bool)

	// SettingsFile is where init writes the hooks, relative to the root,
	// with the OS's separators.
	SettingsFile() string
	// Hooks names the hooks Install writes, in order, for messages.
	Hooks() []string
	// Install returns settings with stele's hooks, stamped with version and
	// carrying sw; changed is false when they are already there.
	Install(settings []byte, version string, sw Switches) (out []byte, changed bool, notes []string, err error)
	// Uninstall returns settings without the hooks Install wrote; empty
	// reports that nothing at all is left.
	Uninstall(settings []byte) (out []byte, removed int, empty bool, err error)
	// Installed returns the version and switches on stele's hooks.
	Installed(settings []byte) (version string, sw Switches, ok bool)
	// OtherSteleHook reports whether settings run stele hook other than as
	// Install writes it for version and sw.
	OtherSteleHook(settings []byte, version string, sw Switches) bool

	// Settings are the project files that configure the agent, as slash
	// paths relative to a directory, SettingsFile's included: the agent
	// loads them from each directory from the session's up to the top of its
	// checkout, so a write to one asks the user first.
	Settings() []string
	// UserSettings are the absolute paths of the user's own settings files,
	// which apply everywhere; nil when they cannot be found.
	UserSettings() []string

	// RulesFile is where stele writes the rules for the agent to load at
	// launch (#68), relative to a root, in slash form.
	RulesFile() string
	// MaxRulesFile caps a read of RulesFile.
	MaxRulesFile() int64
	// MaxRulesChars is the size past which a rules file is worth a warning.
	MaxRulesChars() int
	// RenderRules returns the rules file for rules, in their order.
	RenderRules(rules []Rule) []byte
	// RuleHash is the hash ReadRules gives r's block in a file RenderRules
	// wrote, or "" when r is never taken as read.
	RuleHash(r Rule) string
	// ReadRules returns the rules in a file as the agent read it: rule id to
	// the hash of its block.
	ReadRules(data []byte) map[string]string
	// OwnedRules reports whether data is a rules file stele wrote.
	OwnedRules(data []byte) bool
	// SameRules reports whether the file on disk gives the agent the same
	// rules, in the same order, as want.
	SameRules(disk, want []byte) bool

	// Words names the agent and its tools in stele's messages.
	Words() Words
	// SessionContext is what the agent reads when a session or subagent
	// starts.
	SessionContext() string
}
