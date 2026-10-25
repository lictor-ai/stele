package main

import (
	"maps"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/session"
)

// startsSession reports whether event starts a session or a subagent: hook
// calls that name no file.
func startsSession(event string) bool {
	return event == "SessionStart" || event == "SubagentStart"
}

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

// prime records the rules the agent starting now loaded from
// .claude/rules/stele-rules.md: Claude Code reads that file at launch,
// resume and compaction, and when a subagent starts, before this hook runs.
// So the file as it is now is what the agent read, and it is recorded first;
// only then is a stale file rewritten, for the next session. Recording after
// the rewrite would count rules the agent never saw.
//
// It primes only a session that Claude Code runs in the root itself: whether
// a session in a subdirectory loads the root's file is not measured, and
// priming a rule the agent did not load is the one unsafe mistake. Every
// error leaves the deny to show the rules, as before; none is reported.
func prime(ev claudecode.Event, root string) {
	agent := ""
	if ev.Name == "SubagentStart" {
		if ev.AgentID == "" {
			return // would overwrite the main agent's record
		}
		agent = ev.AgentID
	}
	if cwd, err := filepath.Abs(ev.Cwd); err != nil || filepath.Clean(cwd) != root {
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
