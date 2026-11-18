package backstop

import (
	"time"

	"github.com/lictor-ai/stele/internal/session"
)

// BashFile is a file a shell command writes, with every rule that applies to
// it. The caller passes only files a scoped rule covers.
type BashFile struct {
	File  string // root-relative
	Rules []Rule
}

// BashInput is what the hook knows about one shell command.
type BashInput struct {
	PromptID string
	AgentID  string
	Files    []BashFile
	Start    time.Time        // as for Input
	Now      time.Time        // as for Input
	Window   time.Duration    // as for Input
	Fit      func([]Rule) int // as for Input
}

// BashDecision is the hook's answer to a shell command.
type BashDecision struct {
	Deny  bool
	Files []string // the files denied, in input order
	// Rules are the rules the agent has not read, each once, in input
	// order: never shown, or shown in this batch. Empty when it has read
	// them all, or loaded them at the session's start.
	Rules []Rule
	More  int // unseen rules left out of the message; they stay unseen
}

// DecideBash decides a shell command that writes files with rules (#57). A
// file written through the shell is never shown its rules or checked, so the
// first such command for a file, per agent and turn, is denied: the agent
// is told to use its write tools, and given the rules it has not read. The
// same command run again, at least RetryAfter later, goes through: the file
// may have to come from the command itself.
//
// The deny also counts as a deny of the file's write (Denied), so a write
// the agent makes instead is its retry, and is not denied again.
func DecideBash(in BashInput, st session.State) (BashDecision, session.State) {
	if len(in.Files) == 0 {
		return BashDecision{}, st
	}
	newTurn(&st, in.PromptID)
	shown := st.Shown[in.AgentID]
	c := newClock(in.Start, in.Now, in.Window)
	b := st.Batch[in.AgentID]
	chained, inBatch := c.in(b)

	var d BashDecision
	listed := map[string]bool{}
	var fresh []string
	for _, f := range in.Files {
		// A retry, as for Decide: denied at least RetryAfter before this
		// hook started, not chained to the batch, and not in the future.
		if at, ok := st.BashDenied[in.AgentID][f.File]; ok && !chained && c.start.Sub(at) >= RetryAfter {
			continue
		}
		d.Files = append(d.Files, f.File)
		for _, r := range f.Rules {
			if listed[r.ID] || loaded(st, in.AgentID, r) {
				continue
			}
			at, seen := shown[r.ID]
			switch {
			case !seen:
				fresh = append(fresh, r.ID)
			case inBatch && shownIn(b, at):
				// shown in this batch, not read yet
			default:
				continue // read
			}
			listed[r.ID] = true
			d.Rules = append(d.Rules, r)
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
	st.BashDenied = with(st.BashDenied, in.AgentID, d.Files, c.now)
	st.Denied = with(st.Denied, in.AgentID, d.Files, c.now)
	if len(fresh) > 0 {
		st.Shown = with(st.Shown, in.AgentID, fresh, c.now)
	}
	st.Batch = extend(st.Batch, in.AgentID, inBatch, c.now)
	return d, st
}
