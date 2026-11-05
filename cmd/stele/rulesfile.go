package main

import (
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/scope"
)

// generated is what compile writes under root besides stele.lock and the
// index: the rules file each active agent loads at launch, so the agent has
// the rules before its first write (#68). Every root gets its own, holding
// its own rules. A file there that is not stele's to change is the user's,
// never overwritten, and the file is up to date when it gives the same rules,
// whatever the intro's words: so a stele of another version does not rewrite
// a committed file.
func generated(root string) []compile.Generated {
	var gs []compile.Generated
	for _, a := range activeAgents(root) {
		gs = append(gs, generatedFor(a))
	}
	return gs
}

// generatedFor is agent a's rules file.
func generatedFor(a adapter.Adapter) compile.Generated {
	return compile.Generated{
		Path: a.RulesFile(),
		Render: func(idx index.Index) []byte {
			rules := make([]adapter.Rule, len(idx.Rules))
			for i, e := range idx.Rules {
				rules[i] = agentRule(e)
			}
			return a.RenderRules(rules)
		},
		Splice: a.SpliceRules,
		Same:   a.SameRules,
	}
}

// activeAgents are the agents set up under root: those whose stele hooks are
// installed there, or whose rules file there holds stele's rules. With none,
// the default agent is, so a project gets its rules file before stele init,
// and never one for an agent it does not use (ADR-0012).
func activeAgents(root string) []adapter.Adapter {
	var active []adapter.Adapter
	for _, a := range agents {
		if _, ok := install.InstalledSwitches(a, root); ok {
			active = append(active, a)
			continue
		}
		data, err := safefile.ReadRegular(filepath.Join(root, filepath.FromSlash(a.RulesFile())), a.MaxRulesFile())
		if err == nil && a.OwnedRules(data) {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return agents[:1]
	}
	return active
}

// agentOf is the agent whose rules file is at path, relative to a root.
func agentOf(path string) (adapter.Adapter, bool) {
	for _, a := range agents {
		if a.RulesFile() == path {
			return a, true
		}
	}
	return nil, false
}

// agentRule is an index entry as the rules file shows it.
func agentRule(e index.Entry) adapter.Rule {
	return adapter.Rule{ID: e.ID, Scope: e.Scope, Body: e.Body}
}

// own reports whether rel, relative to a root, is one of stele's own files:
// those check.Own names, and the files compile generates for the agent, such
// as the rules file it loads, which holds every rule's text (#68). A rule is
// never checked against them.
func own(rel string) bool {
	if check.Own(rel) {
		return true
	}
	for _, a := range agents {
		if scope.Match("/"+a.RulesFile(), rel) {
			return true
		}
	}
	return false
}

// backstopRule is an index entry as the backstop decides on it, with the
// hash its block has in the rules file, so a rule the agent loaded from
// there counts as seen (#68).
func backstopRule(e index.Entry) backstop.Rule {
	return backstop.Rule{ID: e.ID, Text: e.Body, Hash: agent.RuleHash(agentRule(e))}
}

// notStele is the message for a generated file's path holding a file stele
// did not write, or, for a file stele writes a part of, one whose part it
// cannot read.
func notStele(path string) string {
	if a, ok := agentOf(path); ok && a.SharedRulesFile() {
		return fmt.Sprintf("%s holds a stele section stele cannot read: fix it or remove it, then run stele compile", path)
	}
	return fmt.Sprintf("%s is not stele's: rename it, then run stele compile", path)
}

// generatedNotes says what the user should know about the generated files
// under root: one that is not stele's, one large enough to cost context in
// every session, and what the agent says of how it reads its file.
func generatedNotes(root string, files []compile.File) []string {
	var notes []string
	for _, f := range files {
		if f.Foreign {
			notes = append(notes, notStele(f.Path))
			continue
		}
		a, ok := agentOf(f.Path)
		if !ok {
			continue
		}
		if n := utf8.RuneCount(f.Data); n > a.MaxRulesChars() {
			notes = append(notes, fmt.Sprintf("%s is %d characters: every session loads it whole, so its rules cost context on every turn; keep stele.md short",
				f.Path, n))
		}
		notes = append(notes, a.RulesNotes(root)...)
	}
	return notes
}
