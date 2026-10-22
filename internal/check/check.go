// Package check is the Checker slot: stele check's runner and report.
//
// A rule with a `check` key names the checker that verifies it; its other
// keys are that checker's settings (D46). Run groups the rules by checker,
// has each checker validate its rules, then runs it on the files that match
// each rule's scope. Whether a violation blocks is read from the rule, never
// from the checker (spec §8).
//
// In v1 the only checker is the built-in regex (ADR-0004). A rule that names
// any other is a Problem, never skipped: a CI job that silently skips a check
// is worse than a red build.
package check

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/scope"
)

// Checker verifies code against the rules that name it.
type Checker interface {
	Name() string
	// Validate reports rules whose settings are wrong, before any file is
	// read. Those rules are left out of Check.
	Validate(rules []Rule) []Problem
	// Check runs rules on files: paths relative to root, with / separators.
	// A file that no longer exists is skipped.
	Check(ctx context.Context, root string, rules []Rule, files []string) ([]Violation, error)
}

// ContentChecker can check content it is handed, without reading a file.
// Only built-in checkers are ContentCheckers; the post-write hook runs them
// and nothing else (D45).
type ContentChecker interface {
	Checker
	CheckContent(rules []Rule, file string, content []byte) []Violation
}

// Rule is what a checker sees of an index.Entry.
type Rule struct {
	ID, Body string
	Scope    []string
	Meta     map[string]string
}

// Problem is an invalid rule, or a rule file stele cannot use. Either fails
// stele check.
type Problem struct {
	RuleID  string `json:"rule_id,omitempty"` // empty for a problem with the rule files
	Excerpt string `json:"excerpt,omitempty"` // filled by Run, never by a checker (spec §12)
	Message string `json:"message"`
}

// Violation is one place a file breaks a rule, as a checker reports it.
type Violation struct {
	RuleID       string
	File         string // relative to the root, with / separators
	Line, Column int    // 1-based; Column counts bytes
	Message      string // empty for the default, the rule's excerpt
}

// Finding is a Violation as stele check reports it.
type Finding struct {
	RuleID   string `json:"rule_id"`
	Excerpt  string `json:"excerpt"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

// Report is the result of a check. It is also stele check --format json.
type Report struct {
	V          int       `json:"v"`       // 1 (D50)
	Project    string    `json:"project"` // D51
	Problems   []Problem `json:"problems"`
	Violations []Finding `json:"violations"`
}

// ReportVersion is the report format's version (D50).
const ReportVersion = 1

// NewReport is an empty report for project.
func NewReport(project string) Report {
	return Report{V: ReportVersion, Project: project, Problems: []Problem{}, Violations: []Finding{}}
}

// ExitCode is stele check's exit code for the report: 1 for an invalid rule
// or a blocking violation, else 0. A Run that failed is 2, decided by the
// caller.
func (r Report) ExitCode() int {
	if len(r.Problems) > 0 {
		return 1
	}
	for _, f := range r.Violations {
		if f.Blocking {
			return 1
		}
	}
	return 0
}

// Run checks files, relative to root, against the rules in idx that name a
// checker in cs. An error means a checker could not run.
func Run(ctx context.Context, root string, idx index.Index, files []string, cs map[string]Checker) (Report, error) {
	p := prepare(idx, cs)
	for _, g := range p.groups {
		var mine []string
		for _, f := range files {
			if !ownFile(f) && g.applies(f) {
				mine = append(mine, f)
			}
		}
		if len(mine) == 0 {
			continue
		}
		vs, err := g.checker.Check(ctx, root, g.rules, mine)
		if err != nil {
			return Report{}, fmt.Errorf("%s: %w", g.checker.Name(), err)
		}
		for _, v := range vs {
			f, ok := g.finding(v)
			if !ok {
				return Report{}, fmt.Errorf("%s: violation of %q, which is not one of its rules", g.checker.Name(), v.RuleID)
			}
			if f != nil {
				p.report.Violations = append(p.report.Violations, *f)
			}
		}
	}
	sortFindings(p.report.Violations)
	return p.report, nil
}

// RunFile checks one file's content without reading anything: the check
// after a write (#29). Only ContentCheckers run; others are skipped.
func RunFile(idx index.Index, file string, content []byte, cs map[string]Checker) Report {
	p := prepare(idx, cs)
	if ownFile(file) {
		return p.report
	}
	for _, g := range p.groups {
		cc, ok := g.checker.(ContentChecker)
		if !ok || !g.applies(file) {
			continue
		}
		for _, v := range cc.CheckContent(g.rules, file, content) {
			if f, _ := g.finding(v); f != nil {
				p.report.Violations = append(p.report.Violations, *f)
			}
		}
	}
	sortFindings(p.report.Violations)
	return p.report
}

// rule is a valid rule with what stele reads of it.
type rule struct {
	Rule
	blocking bool
	excerpt  string
}

// group is one checker and its valid rules.
type group struct {
	checker Checker
	rules   []Rule
	byID    map[string]rule
}

// applies reports whether any of the group's rules applies to file.
func (g group) applies(file string) bool {
	for _, r := range g.rules {
		if scope.Applies(r.Scope, file) {
			return true
		}
	}
	return false
}

// finding turns v into a Finding. ok is false when v names none of the
// group's rules; the finding is nil when v is outside its rule's scope, which
// Run drops whatever the checker does.
func (g group) finding(v Violation) (f *Finding, ok bool) {
	r, ok := g.byID[v.RuleID]
	if !ok {
		return nil, false
	}
	if !scope.Applies(r.Scope, v.File) {
		return nil, true
	}
	msg := v.Message
	if msg == "" {
		msg = r.excerpt
	}
	return &Finding{RuleID: r.ID, Excerpt: r.excerpt, File: v.File, Line: v.Line, Column: v.Column, Message: msg, Blocking: r.blocking}, true
}

type prepared struct {
	groups []group
	report Report
}

// prepare groups idx's checked rules by checker and validates them. Invalid
// rules become problems and are left out; the others still run, so one
// stele check shows every problem.
func prepare(idx index.Index, cs map[string]Checker) prepared {
	p := prepared{report: NewReport(idx.Project)}
	problem := func(r rule, msg string) {
		p.report.Problems = append(p.report.Problems, Problem{RuleID: r.ID, Excerpt: r.excerpt, Message: msg})
	}

	byChecker := map[string][]rule{}
	for _, e := range idx.Rules {
		name, ok := e.Meta["check"]
		if !ok {
			continue // guidance only
		}
		r := rule{Rule: Rule{ID: e.ID, Body: e.Body, Scope: e.Scope, Meta: e.Meta}, blocking: true, excerpt: Excerpt(e.Body)}
		switch b, set := e.Meta["blocking"]; {
		case !set || b == "true":
		case b == "false":
			r.blocking = false
		default:
			problem(r, fmt.Sprintf("blocking: %q is not true or false", b))
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			problem(r, "check is empty")
			continue
		}
		if _, ok := cs[name]; !ok {
			problem(r, fmt.Sprintf("check: %q is not a checker; %s", name, known(cs)))
			continue
		}
		byChecker[name] = append(byChecker[name], r)
	}

	names := make([]string, 0, len(byChecker))
	for name := range byChecker {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rules := byChecker[name]
		g := group{checker: cs[name], byID: map[string]rule{}}
		plain := make([]Rule, len(rules))
		for i, r := range rules {
			plain[i] = r.Rule
		}
		bad := map[string]bool{}
		for _, pr := range g.checker.Validate(plain) {
			for _, r := range rules {
				if r.ID == pr.RuleID {
					pr.Excerpt = r.excerpt
				}
			}
			bad[pr.RuleID] = true
			p.report.Problems = append(p.report.Problems, pr)
		}
		for _, r := range rules {
			if !bad[r.ID] {
				g.rules = append(g.rules, r.Rule)
				g.byID[r.ID] = r
			}
		}
		if len(g.rules) > 0 {
			p.groups = append(p.groups, g)
		}
	}
	return p
}

// known names the checkers a rule can use.
func known(cs map[string]Checker) string {
	names := make([]string, 0, len(cs))
	for name := range cs {
		names = append(names, name)
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "there are none"
	case 1:
		return "the only one is the built-in " + names[0]
	}
	return "the checkers are " + strings.Join(names, ", ")
}

// ownFiles are stele's own files. A rule is never checked against them: a
// pattern quoted in stele.md would otherwise flag stele.md itself.
var ownFiles = []string{"/stele.md", "/stele.json", "/stele.lock", "/" + index.Dir + "/"}

func ownFile(file string) bool {
	for _, p := range ownFiles {
		if scope.Match(p, file) {
			return true
		}
	}
	return false
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.RuleID < b.RuleID
	})
}

// excerptLen bounds an excerpt, in runes.
const excerptLen = 60

// Excerpt is the short text shown beside a rule id: a UUID cannot be read
// (spec §12). It is the body's first paragraph, with its whitespace
// collapsed, cut to about 60 characters.
func Excerpt(body string) string {
	var words []string
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 && len(words) > 0 {
			break // the paragraph ended
		}
		words = append(words, f...)
	}
	s := strings.Join(words, " ")
	if utf8.RuneCountInString(s) <= excerptLen {
		return s
	}
	return string([]rune(s)[:excerptLen]) + "…"
}
