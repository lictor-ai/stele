package main

import (
	"path/filepath"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/session"
	"github.com/lictor-ai/stele/internal/shell"
)

// beforeBash decides a PreToolUse Bash call (#57). A file written through
// Bash is never shown its rules or checked, so a command that writes one a
// scoped rule covers is stopped, once per file and turn, and the agent sent
// to Write and Edit. One that changes stele's own files asks the user, as a
// Write does (#52). stele reads the command; it never runs it, and a
// command that writes nothing costs a parse.
func beforeBash(ev claudecode.Event, sw claudecode.Switches) claudecode.Answer {
	targets, _ := shell.Targets(ev.Command, ev.Cwd)
	var asks, roots []string
	rels := map[string][]string{}
	for _, t := range targets {
		root, rel, ok := target(claudecode.Event{File: t.Path})
		// The Claude Code settings the session loads, wherever they are (#56).
		if !(ok && guarded(root, rel, t.Path)) && loadsSettings(ev, t.Path) {
			asks = append(asks, filepath.ToSlash(t.Path))
			continue
		}
		if !ok {
			// Outside every project, but a link may lead into one.
			if r, ok := resolve(t.Path); ok && r != t.Path {
				if rroot, rrel, ok := target(claudecode.Event{File: r}); ok && guarded(rroot, rrel, r) {
					asks = append(asks, rrel)
				}
			}
			continue
		}
		// Before stele.json is read, as for a Write.
		if guarded(root, rel, t.Path) {
			asks = append(asks, rel)
			continue
		}
		if !t.Kind.Writes() {
			continue // a removal or a copy matters only for stele's own files
		}
		if _, seen := rels[root]; !seen {
			roots = append(roots, root)
		}
		rels[root] = append(rels[root], rel)
	}

	// One answer for the command: any deny wins, as in Claude Code. The
	// projects share one message, so they share its budget.
	var a claudecode.Answer
	budget := install.MaxMessage
	for _, root := range roots {
		r := bashRoot(ev, sw, root, rels[root], budget)
		if r.Deny {
			a.Deny, a.Reason = true, joinNotes(a.Reason, r.Reason)
			budget -= len(r.Reason) + len("\n\n")
		}
		a.Context = joinNotes(a.Context, r.Context)
		a.Warning = joinLines(a.Warning, r.Warning)
	}
	if !a.Deny && len(asks) > 0 {
		a.Ask = install.AskMessage(showPath(strings.Join(asks, ", ")))
	}
	return a
}

// bashRoot decides the files, relative to root, that a Bash command writes
// in one project. Its deny reason holds the rules that fit in budget bytes.
// With --remind=off it never denies: the agent is told the files go
// unchecked.
func bashRoot(ev claudecode.Event, sw claudecode.Switches, root string, rels []string, budget int) claudecode.Answer {
	policy, perr := enforce.Load(root)
	// Whether a file has rules is known only from the index, so a failure
	// here comes before stele can tell go test > out.txt from a source
	// file: it warns, never blocks, even with block_on_error.
	fail := func(err error) claudecode.Answer {
		return claudecode.Answer{Warning: "stele: rules not enforced for this Bash command: " + err.Error()}
	}
	if perr != nil {
		return fail(perr)
	}
	key, err := loadKey()
	if err != nil {
		return fail(err)
	}
	idx, note, err := freshIndex(root, key)
	if err != nil {
		return fail(err)
	}

	// Only a scoped rule stops a command: a rule with no scope covers every
	// file, and would stop go test > out.txt. It is still shown.
	var files []backstop.BashFile
	for _, rel := range rels {
		f := backstop.BashFile{File: rel}
		hit := false
		for _, e := range idx.Rules {
			if scope.Applies(e.Scope, rel) {
				f.Rules = append(f.Rules, backstopRule(e))
				hit = hit || scoped(e.Scope)
			}
		}
		if hit {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return claudecode.Answer{Context: note}
	}
	// With backstop off the hook never blocks (ADR-0004): the agent is told.
	if sw.RemindOff || !policy.Backstop {
		return claudecode.Answer{Context: joinNotes(note, throughBash(files))}
	}

	// Decide inside the update, so the deny is saved before it is answered.
	// The files denied are not known before the decision: the fit counts
	// them all, which only errs on the short side.
	all := make([]string, len(files))
	for i, f := range files {
		all[i] = showPath(f.File)
	}
	fit := func(rs []backstop.Rule) int {
		k := install.FitRules(len(rs), budget, func(k int) string {
			return install.BashMessageMore(all, blockRules(rs[:k]), len(rs)-k, note)
		})
		// The first message shows at least one rule, or a rule too long
		// for any would never be shown; a later project's may show none,
		// and its Write shows them.
		if budget == install.MaxMessage {
			k = max(k, 1)
		}
		return k
	}
	var d backstop.BashDecision
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		in := backstop.BashInput{PromptID: ev.PromptID, AgentID: ev.AgentID, Files: files, Now: now(), Fit: fit}
		d, *st = backstop.DecideBash(in, *st, policy)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if !d.Deny {
		return claudecode.Answer{Context: joinNotes(note, throughBash(files))}
	}
	paths := make([]string, len(d.Files))
	for i, f := range d.Files {
		paths[i] = showPath(f)
	}
	return claudecode.Answer{Deny: true, Reason: install.BashMessageMore(paths, blockRules(d.Rules), d.More, note)}
}

// scoped reports whether patterns name files: a scope with an include, not
// only exclusions.
func scoped(patterns []string) bool {
	for _, p := range patterns {
		if !strings.HasPrefix(p, "!") {
			return true
		}
	}
	return false
}

// throughBash is the note for files a Bash command writes unchecked.
func throughBash(files []backstop.BashFile) string {
	notes := make([]string, len(files))
	for i, f := range files {
		notes[i] = unchecked(showPath(f.File), "it was written through Bash. Run stele check "+showPath(f.File))
	}
	return joinNotes(notes...)
}
