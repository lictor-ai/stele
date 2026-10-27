package main

import (
	"fmt"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
)

// generated is what compile writes besides stele.lock and the index: the
// rules file the agent loads at launch, so the agent has the rules before
// its first write (#68). Every root gets its own, holding its own rules. A
// file there without stele's header is the user's, never overwritten, and
// the file is up to date when it gives the same rules, whatever the intro's
// words: so a stele of another version does not rewrite a committed file.
func generated() []compile.Generated {
	return []compile.Generated{{
		Path: agent.RulesFile(),
		Render: func(idx index.Index) []byte {
			rules := make([]adapter.Rule, len(idx.Rules))
			for i, e := range idx.Rules {
				rules[i] = agentRule(e)
			}
			return agent.RenderRules(rules)
		},
		Owned: agent.OwnedRules,
		Same:  agent.SameRules,
	}}
}

// agentRule is an index entry as the rules file shows it.
func agentRule(e index.Entry) adapter.Rule {
	return adapter.Rule{ID: e.ID, Scope: e.Scope, Body: e.Body}
}

// backstopRule is an index entry as the backstop decides on it, with the
// hash its block has in the rules file, so a rule the agent loaded from
// there counts as seen (#68).
func backstopRule(e index.Entry) backstop.Rule {
	return backstop.Rule{ID: e.ID, Text: e.Body, Hash: agent.RuleHash(agentRule(e))}
}

// notStele is the message for a generated file's path holding a file stele
// did not write.
func notStele(path string) string {
	return fmt.Sprintf("%s is not stele's: rename it, then run stele compile", path)
}

// generatedNotes says what the user should know about the generated files:
// one that is not stele's, and one large enough to cost context in every
// session.
func generatedNotes(files []compile.File) []string {
	var notes []string
	for _, f := range files {
		if f.Foreign {
			notes = append(notes, notStele(f.Path))
			continue
		}
		if n := utf8.RuneCount(f.Data); n > agent.MaxRulesChars() {
			notes = append(notes, fmt.Sprintf("%s is %d characters: every session loads it whole, so its rules cost context on every turn; keep stele.md short",
				f.Path, n))
		}
	}
	return notes
}
