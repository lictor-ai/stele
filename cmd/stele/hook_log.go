package main

import (
	"sort"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/sign"
)

// record appends r to root's log (ADR-0010). Every failure, a panic
// included, is dropped: the log must never change or hold up the answer, so
// it runs after the answer is decided and saved, and its own recover keeps a
// logging bug from turning a deny into runHook's warning.
func record(root string, key sign.Key, r journal.Record) {
	defer func() { _ = recover() }()
	_ = journal.Append(root, key, r)
}

// logHeader is the start of a line for an agent's hook call: the session, and
// the subagent when it is one.
func logHeader(project string, ev adapter.Event, subagent string) journal.Header {
	return journal.Header{Project: project, TS: now().UTC(), Session: ev.SessionID, Agent: agent.Name(), Subagent: subagent}
}

// recordWrote logs a write that went through, with the ids of every rule
// whose scope applies to the file (D70): what stele stats needs to tell a
// rule no write met from one the agent never broke.
func recordWrote(root string, key sign.Key, idx index.Index, ev adapter.Event, rel string) {
	ids := []string{}
	for _, e := range idx.Rules {
		if scope.Applies(e.Scope, rel) {
			ids = append(ids, e.ID)
		}
	}
	record(root, key, &journal.WroteRecord{Header: logHeader(idx.Project, ev, ev.AgentID), Prompt: ev.PromptID, File: rel, Rules: ids})
}

// ruleIDs are the ids of rules.
func ruleIDs(rules []backstop.Rule) []string {
	ids := make([]string, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	return ids
}

// loadedIDs are the ids of the rules an agent loaded.
func loadedIDs(loaded map[string]string) []string {
	ids := make([]string, 0, len(loaded))
	for id := range loaded {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
