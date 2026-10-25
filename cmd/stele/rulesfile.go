package main

import (
	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
)

// generated is what compile writes besides stele.lock and the index: the
// rules file Claude Code loads at launch, so the agent has the rules before
// its first write (#68). Every root gets its own, holding its own rules.
func generated() []compile.Generated {
	return []compile.Generated{{
		Path: claudecode.RulesFile,
		Render: func(idx index.Index) []byte {
			rules := make([]claudecode.Rule, len(idx.Rules))
			for i, e := range idx.Rules {
				rules[i] = ccRule(e)
			}
			return claudecode.RenderRules(rules)
		},
	}}
}

// ccRule is an index entry as the rules file shows it.
func ccRule(e index.Entry) claudecode.Rule {
	return claudecode.Rule{ID: e.ID, Scope: e.Scope, Body: e.Body}
}
