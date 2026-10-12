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
// agent reads the rules once and retries all of it. It is shorter than any
// model round trip, so a retry after reading goes through.
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
	}
	shown := st.Shown[in.AgentID]

	var show []Rule
	var fresh []string
	for _, r := range in.Rules {
		at, seen := shown[r.ID]
		switch {
		case !seen:
			show = append(show, r)
			fresh = append(fresh, r.ID)
		case in.Now.Sub(at) < Window:
			show = append(show, r) // same batch: shown, but not read yet
		}
	}
	if len(show) == 0 {
		return Decision{}, st
	}

	// Copy before writing, so the caller's state is never changed in place.
	next := make(map[string]map[string]time.Time, len(st.Shown)+1)
	maps.Copy(next, st.Shown)
	agent := maps.Clone(shown)
	if agent == nil {
		agent = make(map[string]time.Time, len(fresh))
	}
	for _, id := range fresh {
		agent[id] = in.Now
	}
	next[in.AgentID] = agent
	st.Shown = next
	return Decision{Deny: true, Rules: show}, st
}
