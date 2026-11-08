// Package rulelist is what stele rules prints: the compiled rules, each with
// its text, scope and source, for other tools (D86, ADR-0015).
//
// It reads nothing itself. cmd/stele hands it the index, brought up to date
// as the hook does, and stele.lock; Build joins them. The JSON is a public
// format with a version: a field may be added within Version, a change of
// meaning bumps it.
package rulelist

import (
	"strings"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// Version is the format of List.
const Version = 1

// The kinds of source a rule comes from.
const (
	KindSteleMD = "stele.md" // the project's own stele.md
	KindGit     = "git"      // the git source: another repository (ADR-0011)
	KindPlugin  = "plugin"   // an external source plugin (ADR-0013)
)

// steleMD is the file the stele.md source reads.
const steleMD = "stele.md"

// List is the compiled rules.
type List struct {
	V       int    `json:"v"`
	Project string `json:"project"`
	// Rules are in index order: stele.md first, then the other sources, the
	// order of the agent's rules file.
	Rules []Rule `json:"rules"`
	// Missing are the sources stele.lock pins or holds rules for that the
	// index lacks, such as on a fresh clone: stele compile reads them.
	Missing []Source `json:"missing"`
	// Notes are what the reader should know, such as a stele.md that does
	// not parse, whose last valid rules are listed. cmd/stele adds them.
	Notes []string `json:"notes"`
}

// Rule is one compiled rule.
type Rule struct {
	ID   string `json:"id"`
	Text string `json:"text"` // the rule's body, as written
	// Scope is the rule's globs, or nil, null in JSON, for every file.
	Scope  []string `json:"scope"`
	Source Source   `json:"source"`
}

// Source is where a rule came from.
type Source struct {
	Kind string `json:"kind"`
	Name string `json:"name"` // the source's name in stele.lock, a stable key
	// Repo, Ref and Commit are a git source's: the repository as stele.json
	// sets it, the branch or tag ("" for the remote's default branch), and
	// the commit the text was read at.
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	File   string `json:"file,omitempty"` // the rule file, in its repository
	Commit string `json:"commit,omitempty"`
	// Rule is the rule's 1-based place in its source, as stele compile
	// names it. 0, left out, in Missing.
	Rule int `json:"rule,omitempty"`
}

// Build lists the rules of idx, with their sources from idx and lf, and the
// sources lf holds that idx lacks. It shares no slice with idx.
func Build(idx index.Index, lf lock.File) List {
	pins := make(map[string]source.Pin, len(lf.Sources))
	for _, p := range lf.Sources {
		pins[p.Source] = p
	}
	l := List{V: Version, Project: idx.Project, Rules: []Rule{}, Missing: []Source{}, Notes: []string{}}
	held := make(map[string]bool, len(idx.Rules))
	for _, e := range idx.Rules {
		held[e.ID] = true
		src := sourceOf(e.Source, pins)
		if src.Kind == KindGit {
			src.Commit = idx.Sources[e.Source]
		}
		src.Rule = e.Position + 1
		var scope []string
		if len(e.Scope) > 0 {
			scope = append([]string(nil), e.Scope...)
		}
		l.Rules = append(l.Rules, Rule{ID: e.ID, Text: e.Body, Scope: scope, Source: src})
	}

	listed := map[string]bool{}
	miss := func(name string) {
		if !listed[name] {
			listed[name] = true
			l.Missing = append(l.Missing, sourceOf(name, pins))
		}
	}
	// A pinned source the index holds at another commit, or not at all.
	for _, p := range lf.Sources {
		if fp, ok := idx.Sources[p.Source]; !ok || fp != p.Commit {
			miss(p.Source)
		}
	}
	for _, e := range lf.Rules {
		if !held[e.ID] {
			miss(e.Source)
		}
	}
	return l
}

// sourceOf describes the source named name; a git source's settings come
// from its pin, its commit from the pin too, which Build replaces with the
// index's for a rule.
func sourceOf(name string, pins map[string]source.Pin) Source {
	switch {
	case name == stelemd.New().Name():
		return Source{Kind: KindSteleMD, Name: name, File: steleMD}
	case strings.HasPrefix(name, gitrepo.Name+":"):
		p := pins[name]
		return Source{Kind: KindGit, Name: name, Repo: p.Repo, Ref: p.Ref, File: p.Path, Commit: p.Commit}
	}
	return Source{Kind: KindPlugin, Name: name}
}
