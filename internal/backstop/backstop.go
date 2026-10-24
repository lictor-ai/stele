// Package backstop decides the hook outcome. It does no IO.
//
// Before a write, the agent must have seen every rule that applies to the
// file. Decide denies a write that would bring rules the agent has not been
// shown this turn, and the caller sends those rules as the deny reason
// (ADR-0004). Given the rules, the session state, the policy and the time, it
// returns the decision and the state to save, so the most behaviour-dense part
// of the hook is a table test.
package backstop

import (
	"maps"
	"time"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/session"
)

// Window is how long after a rule is shown a write that needs it is still
// denied. The agent often issues several writes at once, and their hooks run
// before it reads any deny reason; the window denies the whole batch, so the
// agent reads the rules once and retries all of it. A model can retry within
// the window, so a retry is recognised by its file, not by time: a write to
// a file whose write was denied comes after that deny's reason was read.
// A time before a rule was shown (the clock moved back) is outside it.
const Window = 3 * time.Second

// Rule is a rule that applies to the file about to be written.
type Rule struct {
	ID   string
	Text string // what the agent is shown
}

// Input is what the hook knows about one write.
type Input struct {
	PromptID string    // the turn; a new one starts with no rules shown
	AgentID  string    // "" for the main agent; a subagent has its own record
	File     string    // root-relative path of the file written; "" disables retry detection
	Rules    []Rule    // every rule that applies to the file
	Now      time.Time // when the hook runs
}

// Decision is the hook's answer.
type Decision struct {
	Deny  bool
	Rules []Rule // when denying, the rules to show, in input order
}

// Decide returns the decision for one write, and the state to save before the
// decision is answered (ADR-0004: save before deny).
func Decide(in Input, st session.State, p enforce.Policy) (Decision, session.State) {
	if !p.Backstop || len(in.Rules) == 0 {
		return Decision{}, st
	}
	if in.PromptID != st.PromptID {
		st.PromptID = in.PromptID
		st.Shown = nil
		st.Denied = nil
	}
	shown := st.Shown[in.AgentID]
	// A retry: this file's write was denied, so the agent has read that
	// reason, which held every rule for the file shown at or before it.
	deniedAt, retry := st.Denied[in.AgentID][in.File]
	retry = retry && in.File != ""

	var show []Rule
	var fresh []string
	for _, r := range in.Rules {
		at, seen := shown[r.ID]
		switch {
		case !seen:
			show = append(show, r)
			fresh = append(fresh, r.ID)
		case retry && !at.After(deniedAt):
			// read in this file's deny reason, or before it
		case inWindow(in.Now, at):
			show = append(show, r) // same batch: shown, but not read yet
		}
	}
	if len(show) == 0 {
		return Decision{}, st
	}

	// Copy before writing, so the caller's state is never changed in place.
	st.Shown = with(st.Shown, in.AgentID, fresh, in.Now)
	if in.File != "" {
		st.Denied = with(st.Denied, in.AgentID, []string{in.File}, in.Now)
	}
	return Decision{Deny: true, Rules: show}, st
}

// inWindow reports whether now is within Window after at. A now before at
// means the clock moved back, and is outside: the rule counts as read.
func inWindow(now, at time.Time) bool {
	d := now.Sub(at)
	return d >= 0 && d < Window
}

// with returns a copy of m in which agent's keys are stamped at, leaving m
// and its inner maps unchanged.
func with(m map[string]map[string]time.Time, agent string, keys []string, at time.Time) map[string]map[string]time.Time {
	next := make(map[string]map[string]time.Time, len(m)+1)
	maps.Copy(next, m)
	inner := maps.Clone(m[agent])
	if inner == nil {
		inner = make(map[string]time.Time, len(keys))
	}
	for _, k := range keys {
		inner[k] = at
	}
	next[agent] = inner
	return next
}
