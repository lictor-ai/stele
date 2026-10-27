package claudecode

import "path/filepath"

// Loaded says whether the agent starting now, at a SessionStart or
// SubagentStart, loaded root's rules file, as which agent ("" for the main
// one), and whether this start launches the session in root.
//
// Claude Code reads .claude/rules at launch, resume and compaction, and when
// a subagent starts, before the hook runs. It primes only a session that
// Claude Code runs in the root itself: whether a session in a subdirectory
// loads the root's file is not measured, and priming a rule the agent did
// not load is the one unsafe mistake. A subagent is primed only when its
// type is measured to load the file; one with no agent id would overwrite
// the main agent's record.
func Loaded(ev Event, root string) (agent string, launch, ok bool) {
	switch {
	case ev.Name == "SubagentStart":
		if ev.AgentID == "" || !loadsRules[ev.AgentType] {
			return "", false, false
		}
		agent = ev.AgentID
	case ev.Source == "startup" || ev.Source == "resume":
		launch = true
	}
	if cwd, err := filepath.Abs(ev.Cwd); err != nil || filepath.Clean(cwd) != root {
		return "", false, false
	}
	return agent, launch, true
}

// loadsRules lists the subagent types measured to load .claude/rules when
// they start (Claude Code 2.1.310, 2026-10-25): general-purpose loads it;
// the built-in Explore and Plan do not. Any other type, a custom agent's
// included, is not primed until measured.
var loadsRules = map[string]bool{"general-purpose": true}
