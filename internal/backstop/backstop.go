// Package backstop decides the hook outcome. It does no IO.
//
// Before a write, the agent must have seen every rule that applies to the
// file. Decide denies a write that would bring rules the agent has not been
// shown this turn, and the caller sends those rules as the deny reason
// (ADR-0004). Given the rules, the session state and the time, it
// returns the decision and the state to save, so the most behaviour-dense part
// of the hook is a table test.
//
// # Batches
//
// A batch is the writes the agent issues at once, in one message: their
// hooks run before it reads any deny reason, so a rule one deny of the batch
// showed is not read yet when the next hook runs, and that write is denied
// with it too. The agent reads the rules once and retries the whole batch.
//
// The turn (PromptID) cannot tell a batch apart: one turn holds many model
// round trips. So a batch is told by when each hook started, taken before it
// waited for the session lock or anything else, never by when it got the
// lock: in a batch of many, a hook can wait seconds for the others (#151). A
// hook is in the batch of the agent's last deny when
//
//   - it started before that deny was answered, or less than RetryAfter
//     after it (chained): the agent cannot have read it; or
//   - it started less than Window after the batch's first deny, which covers
//     the hooks of a batch the agent starts slowly.
//
// A retry is recognised by its file: a write to a file whose write was
// denied, started at least RetryAfter after that deny, comes after the agent
// read that deny's reason, unless it is chained to the same chain of denies
// as the deny: then it is a second write to the file in one message. A chain
// is a run of denies each less than RetryAfter after the one before
// (Batch.Chain is its first); a write chained to a later chain, such as a
// retry sent together with a write that needs a new rule, is still a retry.
package backstop

import (
	"maps"
	"time"

	"github.com/lictor-ai/stele/internal/session"
)

// Window is how long after a batch's first deny a write is still taken as
// part of the batch, however its hook was delayed. The chain covers hooks
// that follow each other closely; this covers a batch the agent starts
// slowly. A time before the batch's first deny is not in the window.
const Window = 3 * time.Second

// RetryAfter is how long after a deny a write counts as the agent's next
// step, made after it read the reason. The hooks of parallel tool calls run
// within milliseconds of each other; a retry needs a model round trip, and
// the fastest seen took 1.2 s. A write that started sooner than this after
// the batch's last deny is in the batch.
const RetryAfter = 500 * time.Millisecond

// Lead is how long before a batch's first deny a hook of that batch can have
// started: it was waiting for the hooks before it. A hook that started
// earlier is not chained to the batch: the clock moved back, and chaining it
// could deny a write until the clock caught up.
const Lead = 10 * time.Second

// Rule is a rule that applies to the file about to be written.
type Rule struct {
	ID   string
	Text string // what the agent is shown
	// Hash is the hash of the rule's block in the rules file the agent
	// loads at launch (#68); "" when it can never count as loaded.
	Hash string
}

// loaded reports whether agent loaded r, as it reads now, from the rules
// file: then it is in the agent's context for the whole session.
func loaded(st session.State, agent string, r Rule) bool {
	return r.Hash != "" && st.Primed[agent][r.ID] == r.Hash
}

// Input is what the hook knows about one write.
type Input struct {
	PromptID string // the turn; a new one starts with no rules shown
	AgentID  string // "" for the main agent; a subagent has its own record
	File     string // root-relative path of the file written; "" disables retry detection
	Rules    []Rule // every rule that applies to the file
	// Start is when the hook started, before it waited for anything; a
	// batch is told by it. Zero, or after Now, means Now.
	Start time.Time
	Now   time.Time // when the hook decides, holding the session lock
	// Window replaces the package's Window when positive.
	Window time.Duration
	// Fit returns how many of the rules to show, in order, fit in the
	// message; nil fits them all. With none, the deny shows only a count.
	Fit func([]Rule) int
}

// Decision is the hook's answer.
type Decision struct {
	Deny  bool
	Rules []Rule // when denying, the rules to show, in input order
	More  int    // rules left out of the message; they stay unseen
}

// Decide returns the decision for one write, and the state to save before the
// decision is answered (ADR-0004: save before deny).
func Decide(in Input, st session.State) (Decision, session.State) {
	if len(in.Rules) == 0 {
		return Decision{}, st
	}
	newTurn(&st, in.PromptID)
	shown := st.Shown[in.AgentID]
	c := newClock(in.Start, in.Now, in.Window)
	b := st.Batch[in.AgentID]
	chained, inBatch := c.in(b)
	// A retry: this file's write was denied at least RetryAfter before this
	// hook started, and the batch has ended, so the agent has read that
	// reason, which held every rule for the file shown at or before it.
	// Sooner is a parallel write to the same file, and a deny in the future
	// (the clock moved back) proves nothing.
	deniedAt, retry := st.Denied[in.AgentID][in.File]
	retry = retry && in.File != "" && retried(c, b, chained, deniedAt)

	var show []Rule
	var fresh []string
	for _, r := range in.Rules {
		at, seen := shown[r.ID]
		switch {
		case loaded(st, in.AgentID, r):
			// loaded at the session's start, unchanged since
		case !seen:
			show = append(show, r)
			fresh = append(fresh, r.ID)
		case retry && !at.After(deniedAt):
			// read in this file's deny reason, or before it
		case inBatch && shownIn(b, at):
			show = append(show, r) // same batch: shown, but not read yet
		}
	}
	if len(show) == 0 {
		return Decision{}, st
	}
	// Only the rules the message holds are shown: the rest stay unseen, so
	// the next write shows them.
	k := fit(in.Fit, show)
	more := len(show) - k
	show, fresh = show[:k], kept(fresh, show[:k])

	// Copy before writing, so the caller's state is never changed in place.
	st.Shown = with(st.Shown, in.AgentID, fresh, c.now)
	if in.File != "" {
		st.Denied = with(st.Denied, in.AgentID, []string{in.File}, c.now)
	}
	st.Batch = extend(st.Batch, in.AgentID, inBatch, chained, c.now)
	return Decision{Deny: true, Rules: show, More: more}, st
}

// clock is when a hook started and decided, and the window it uses.
type clock struct {
	start, now time.Time
	window     time.Duration
}

func newClock(start, now time.Time, window time.Duration) clock {
	if start.IsZero() || start.After(now) {
		start = now
	}
	if window <= 0 {
		window = Window
	}
	return clock{start: start, now: now, window: window}
}

// in reports whether a hook with this clock is chained to batch b, and
// whether it is in b at all (chained, or in b's window). No batch holds
// nothing.
func (c clock) in(b session.Batch) (chained, in bool) {
	if b.End.IsZero() {
		return false, false
	}
	chained = c.start.Sub(b.End) < RetryAfter && b.Start.Sub(c.start) <= Lead
	d := c.start.Sub(b.Start)
	return chained, chained || (d >= 0 && d < c.window)
}

// shownIn reports whether a rule shown at at was shown in batch b.
func shownIn(b session.Batch, at time.Time) bool {
	return !b.End.IsZero() && !at.Before(b.Start) && !at.After(b.End)
}

// retried reports whether a write that started with clock c, to a file
// denied at deniedAt, is the agent's retry: it started at least RetryAfter
// after the deny, and is not chained to the chain of denies that holds it.
// A deny before the current chain began was read when the agent sent it.
func retried(c clock, b session.Batch, chained bool, deniedAt time.Time) bool {
	return c.start.Sub(deniedAt) >= RetryAfter && (!chained || deniedAt.Before(b.Chain))
}

// extend returns a copy of m in which agent's batch takes a deny decided at
// now: the batch goes on when the write was in it, else a new one starts;
// the chain goes on when the write was chained, else a new one starts. End
// is the last deny's time, even one before it: after the clock moved back
// it moves back too, which only shortens the chain.
func extend(m map[string]session.Batch, agent string, inBatch, chained bool, now time.Time) map[string]session.Batch {
	next := make(map[string]session.Batch, len(m)+1)
	maps.Copy(next, m)
	b := next[agent]
	if !inBatch || b.End.IsZero() {
		b.Start = now
	}
	if !chained || b.Chain.IsZero() {
		b.Chain = now
	}
	b.End = now
	next[agent] = b
	return next
}

// fit returns how many of rs f fits: all with no f.
func fit(f func([]Rule) int, rs []Rule) int {
	if f == nil {
		return len(rs)
	}
	return min(max(f(rs), 0), len(rs))
}

// kept returns the ids of ids that are among rs.
func kept(ids []string, rs []Rule) []string {
	in := make(map[string]bool, len(rs))
	for _, r := range rs {
		in[r.ID] = true
	}
	var out []string
	for _, id := range ids {
		if in[id] {
			out = append(out, id)
		}
	}
	return out
}

// newTurn forgets what the last turn showed and denied when prompt starts a
// new one. Everything a turn records is cleared here, so a record of either
// kind of write cannot outlive its turn.
func newTurn(st *session.State, prompt string) {
	if prompt != st.PromptID {
		st.PromptID = prompt
		st.Shown, st.Denied, st.BashDenied, st.Batch = nil, nil, nil, nil
	}
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
