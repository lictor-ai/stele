package backstop

import (
	"time"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/session"
)

// BashFile is a file a Bash command writes, with every rule that applies to
// it. The caller passes only files a scoped rule covers.
type BashFile struct {
	File  string // root-relative
	Rules []Rule
}

// BashInput is what the hook knows about one Bash command.
type BashInput struct {
	PromptID string
	AgentID  string
	Files    []BashFile
	Now      time.Time
	Fit      func([]Rule) int // as for Input
}

// BashDecision is the hook's answer to a Bash command.
type BashDecision struct {
	Deny  bool
	Files []string // the files denied, in input order
	Rules []Rule   // the rules the agent has not been shown, each once, in input order
	More  int      // unseen rules left out of the message; they stay unseen
}

// DecideBash decides a Bash command that writes files with rules (#57). A
// file written through Bash is never shown its rules or checked, so the
// first such command for a file, per agent and turn, is denied: the agent
// is told to use Write or Edit, and given the rules it has not seen. The
// same command run again, at least RetryAfter later, goes through: the file
// may have to come from the command itself.
//
// The deny also counts as a deny of the file's Write (Denied), so a Write
// the agent makes instead is its retry, and is not denied again.
func DecideBash(in BashInput, st session.State, p enforce.Policy) (BashDecision, session.State) {
	if !p.Backstop || len(in.Files) == 0 {
		return BashDecision{}, st
	}
	newTurn(&st, in.PromptID)
	shown := st.Shown[in.AgentID]

	var d BashDecision
	listed := map[string]bool{}
	var fresh []string
	for _, f := range in.Files {
		// A retry, as for Decide: denied at least RetryAfter ago, and not in
		// the future.
		if at, ok := st.BashDenied[in.AgentID][f.File]; ok && in.Now.Sub(at) >= RetryAfter {
			continue
		}
		d.Files = append(d.Files, f.File)
		for _, r := range f.Rules {
			if _, seen := shown[r.ID]; !seen && !listed[r.ID] {
				listed[r.ID] = true
				d.Rules = append(d.Rules, r)
				fresh = append(fresh, r.ID)
			}
		}
	}
	if len(d.Files) == 0 {
		return BashDecision{}, st
	}
	d.Deny = true
	// Only the rules the message holds are shown; a Write shows the rest.
	k := fit(in.Fit, d.Rules)
	d.More = len(d.Rules) - k
	d.Rules, fresh = d.Rules[:k], kept(fresh, d.Rules[:k])
	// Copy before writing, so the caller's state is never changed in place.
	st.BashDenied = with(st.BashDenied, in.AgentID, d.Files, in.Now)
	st.Denied = with(st.Denied, in.AgentID, d.Files, in.Now)
	if len(fresh) > 0 {
		st.Shown = with(st.Shown, in.AgentID, fresh, in.Now)
	}
	return d, st
}
