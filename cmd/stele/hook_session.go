package main

import (
	"maps"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/session"
)

// sessionStart tells the agent, before its first prompt, to write files with
// Write and Edit only. Agents in tests wrote whole files through Bash, which
// stele never sees; the deny message alone comes too late when that first
// write is a Bash one (#50). It also records which rules the agent loaded
// from the rules file (#68). It says nothing outside a stele project.
func sessionStart(ev claudecode.Event) claudecode.Answer {
	if ev.Cwd == "" {
		return claudecode.Answer{}
	}
	root, err := findRoot(ev.Cwd)
	if err != nil || !usesStele(root) {
		return claudecode.Answer{}
	}
	prime(ev, root)
	return claudecode.Answer{Context: install.SessionContext()}
}

// maxPrimed caps the agents whose loaded rules a session keeps, so a session
// with many subagents cannot grow its state without bound. Dropping one
// costs denies, never a missed rule.
const maxPrimed = 32

// prime records the rules the agent starting now loaded from the rules file,
// as the adapter says it did (claudecode.Loaded): the agent reads that file
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
func prime(ev claudecode.Event, root string) {
	agent, launch, ok := claudecode.Loaded(ev, root)
	if !ok {
		return
	}
	key, err := loadKey()
	if err != nil {
		return
	}
	loaded := map[string]string{}
	data, err := safefile.ReadRegular(filepath.Join(root, filepath.FromSlash(claudecode.RulesFile)), claudecode.MaxRulesFile)
	if err == nil && claudecode.OwnedRules(data) {
		loaded = claudecode.ReadRules(data)
	}
	_, _ = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		if launch {
			st.LaunchRoot = true
		}
		if !st.LaunchRoot {
			// Launched elsewhere: the agent reloaded that directory's
			// rules, not this root's file.
			if agent == "" {
				st.Primed = withPrimed(st.Primed, "", nil)
			}
			return nil
		}
		st.Primed = withPrimed(st.Primed, agent, loaded)
		return nil
	})
	_, _, _ = freshIndex(root, key)
}

// withPrimed returns a copy of m in which agent's record is loaded, replacing
// what the agent loaded before: Claude Code read the file again. Past
// maxPrimed agents, the other subagents' records are dropped.
func withPrimed(m map[string]map[string]string, agent string, loaded map[string]string) map[string]map[string]string {
	next := maps.Clone(m)
	if next == nil {
		next = map[string]map[string]string{}
	}
	delete(next, agent)
	if len(loaded) > 0 {
		next[agent] = loaded
	}
	if len(next) > maxPrimed {
		for a := range next {
			if a != "" && a != agent {
				delete(next, a)
			}
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}
