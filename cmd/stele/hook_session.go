package main

import (
	"maps"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/session"
)

// sessionStart tells the agent, before its first prompt, to write files with
// Write and Edit only. Agents in tests wrote whole files through Bash, which
// stele never sees; the deny message alone comes too late when that first
// write is a Bash one (#50). It also records which rules the agent loaded
// from the rules file (#68). It says nothing outside a stele project.
func sessionStart(ev adapter.Event) adapter.Answer {
	if ev.Cwd == "" {
		return adapter.Answer{}
	}
	root, err := findRoot(ev.Cwd)
	if err != nil || !usesStele(root) {
		return adapter.Answer{}
	}
	prime(ev, root)
	return adapter.Answer{Context: agent.SessionContext()}
}

// maxPrimed caps the agents whose loaded rules a session keeps, so a session
// with many subagents cannot grow its state without bound. Dropping one
// costs denies, never a missed rule.
const maxPrimed = 32

// prime records the rules the agent starting now loaded from the rules file,
// as the adapter says it did (Adapter.Loaded): the agent reads that file
// before this hook runs, so the file as it is now is what the agent read,
// and it is recorded first; only then is a stale file rewritten, for the
// next session. Recording after the rewrite would count rules the agent
// never saw.
//
// The agent reloads the file from the directory it was launched in, while
// the hook's cwd follows the agent's cd: so a launch marks this root as the
// session's launch root, and any other start primes only a root so marked.
// Every error leaves the deny to show the rules, as before; none is
// reported.
func prime(ev adapter.Event, root string) {
	id, launch, ok := agent.Loaded(ev, root)
	if !ok {
		return
	}
	key, err := loadKey()
	if err != nil {
		return
	}
	loaded := map[string]string{}
	data, err := safefile.ReadRegular(filepath.Join(root, filepath.FromSlash(agent.RulesFile())), agent.MaxRulesFile())
	if err == nil && agent.OwnedRules(data) {
		loaded = agent.ReadRules(data)
	}
	_, _ = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		if launch {
			st.LaunchRoot = true
		}
		if !st.LaunchRoot {
			// Launched elsewhere: the agent reloaded that directory's
			// rules, not this root's file.
			if id == "" {
				st.Primed = withPrimed(st.Primed, "", nil)
			}
			return nil
		}
		st.Primed = withPrimed(st.Primed, id, loaded)
		return nil
	})
	_, _, _ = freshIndex(root, key)
}

// withPrimed returns a copy of m in which agent id's record is loaded,
// replacing what it loaded before: the agent read the file again. Past
// maxPrimed agents, the other subagents' records are dropped.
func withPrimed(m map[string]map[string]string, id string, loaded map[string]string) map[string]map[string]string {
	next := maps.Clone(m)
	if next == nil {
		next = map[string]map[string]string{}
	}
	delete(next, id)
	if len(loaded) > 0 {
		next[id] = loaded
	}
	if len(next) > maxPrimed {
		for a := range next {
			if a != "" && a != id {
				delete(next, a)
			}
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}
