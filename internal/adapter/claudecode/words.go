package claudecode

import (
	_ "embed"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
)

// words name Claude Code's tools in stele's messages: it writes files with
// Write, Edit and MultiEdit, which the hooks see, and runs commands with
// Bash, which they do not.
var words = adapter.Words{
	Agent:        "Claude Code",
	Shell:        "Bash",
	WriteTools:   "Write and Edit",
	WriteInstead: "the Write or Edit tool instead (MultiEdit too)",
}

// sessionContext is what the agent reads at the start of a session or
// subagent in a stele project: write files with Write and Edit, which stele
// sees, not through Bash, which it does not (#50).
//
//go:embed assets/session-context.tmpl
var sessionContext string

// SessionContext returns the text the agent reads when a session or subagent
// starts.
func SessionContext() string { return strings.TrimSpace(sessionContext) }
