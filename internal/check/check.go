// Package check is the Checker slot: stele check's runner and report.
//
// A rule with a `check` key names the checker that verifies it; its other
// keys are that checker's settings (D46). Run groups the rules by checker,
// has each checker validate its rules, then runs it on the files that match
// each rule's scope. Whether a violation blocks is read from the rule, never
// from the checker (spec §8).
//
// The built-in checker is regex; others are external plugins listed in
// stele.json (ADR-0013). A rule that names a checker not there is a Problem,
// never skipped: a CI job that silently skips a check is worse than a red
// build.
package check

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/source"
)

// Checker verifies code against the rules that name it.
type Checker interface {
	Name() string
	// Validate reports rules whose settings are wrong, before any file is
	// read. Those rules are left out of Check. An error means the checker
	// could not say, such as an external one that failed (D80).
	Validate(ctx context.Context, rules []Rule) ([]Problem, error)
	// Check runs rules on files: paths relative to root, with / separators.
	// A file it cannot check, such as one that no longer exists, is skipped
	// and named in the result.
	Check(ctx context.Context, root string, rules []Rule, files []string) (Result, error)
}

// ContentChecker can check content it is handed, without reading a file.
// Only built-in checkers are ContentCheckers; the post-write hook runs them
// and nothing else (D45).
type ContentChecker interface {
	Checker
	CheckContent(rules []Rule, file string, content []byte) Result
}

// Result is what a checker found, and the files it did not check.
type Result struct {
	Violations []Violation
	Skipped    []Skip
}

// Skip is a file that was not checked, and why. A skip is never a pass: it is
// reported, so a file cannot slip past CI unseen. It does not change the exit
// code.
type Skip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
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
	Line, Column int    // 1-based; Column counts characters (runes)
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
	Skipped    []Skip    `json:"skipped"`
	// Truncated counts the violations left out past maxPerFile for one rule
	// in one file.
	Truncated int `json:"truncated"`
	// RuleFilesChanged is set only by stele check --base, and has no effect
	// on the exit code.
	RuleFilesChanged []RuleFileChange `json:"rule_files_changed,omitempty"`
}

// RuleFileChange is a stele.md or stele.json that stele check --base found
// changed: reviewers must see a change to the rules, or to how strictly they
// are enforced.
type RuleFileChange struct {
	File   string `json:"file"`   // relative to the root, / separators
	Change string `json:"change"` // "added", "modified" or "deleted"
}

// ReportVersion is the report format's version (D50).
const ReportVersion = 1

// NewReport is an empty report for project.
func NewReport(project string) Report {
	return Report{V: ReportVersion, Project: project, Problems: []Problem{}, Violations: []Finding{}, Skipped: []Skip{}}
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
	p, err := prepare(ctx, idx, cs, false)
	if err != nil {
		return Report{}, err
	}
	for _, g := range p.groups {
		var mine []string
		for _, f := range files {
			if !Own(f) && g.applies(f) {
				mine = append(mine, f)
			}
		}
		if len(mine) == 0 {
			continue
		}
		res, err := g.checker.Check(ctx, root, g.rules, mine)
		if err != nil {
			return Report{}, checkerErr(g.checker.Name(), err)
		}
		p.report.Skipped = append(p.report.Skipped, res.Skipped...)
		for _, v := range res.Violations {
			f, ok := g.finding(v)
			if !ok {
				return Report{}, fmt.Errorf("%s: violation of %q, which is not one of its rules", g.checker.Name(), v.RuleID)
			}
			if f != nil {
				p.report.Violations = append(p.report.Violations, *f)
			}
		}
	}
	p.report.finish()
	return p.report, nil
}

// Validate reports the rules in idx that cannot be checked, as Run would,
// reading no file: a bad blocking, an empty or unknown check, or settings
// the checker rejects. Excerpts are filled. An error means a checker could
// not validate its rules.
func Validate(ctx context.Context, idx index.Index, cs map[string]Checker) ([]Problem, error) {
	p, err := prepare(ctx, idx, cs, false)
	return p.report.Problems, err
}

// RunFile checks one file's content without reading anything. Only
// ContentCheckers run; others are skipped.
func RunFile(idx index.Index, file string, content []byte, cs map[string]Checker) Report {
	return RunFileLines(idx, file, content, cs, nil)
}

// RunFileLines is RunFile keeping only the violations that start on a line
// keep accepts: the check after a write (#29), where only the agent's lines
// count. The filter runs before the cap per rule and file, so a file that
// already breaks a rule many times still shows the agent's own violation. A
// nil keep keeps all.
func RunFileLines(idx index.Index, file string, content []byte, cs map[string]Checker, keep func(line int) bool) Report {
	r, _ := RunFileChecked(idx, file, content, cs, keep)
	return r
}

// RunFileChecked is RunFileLines also returning the ids, sorted, of the
// rules the file was checked against: valid rules in scope whose checker
// ran on it and did not skip it. A clean check is still a check of these
// rules, which the log records (ADR-0010); a binary file was checked against
// none.
func RunFileChecked(idx index.Index, file string, content []byte, cs map[string]Checker, keep func(line int) bool) (Report, []string) {
	// Only ContentCheckers are asked to validate: they are built in, and
	// start nothing (D45). They cannot fail to.
	p, _ := prepare(context.Background(), idx, cs, true)
	if Own(file) {
		return p.report, nil
	}
	var ids []string
	for _, g := range p.groups {
		cc, ok := g.checker.(ContentChecker)
		if !ok || !g.applies(file) {
			continue
		}
		res := cc.CheckContent(g.rules, file, content)
		p.report.Skipped = append(p.report.Skipped, res.Skipped...)
		if !skips(res.Skipped, file) {
			for _, r := range g.rules {
				if scope.Applies(r.Scope, file) {
					ids = append(ids, r.ID)
				}
			}
		}
		for _, v := range res.Violations {
			if keep != nil && !keep(v.Line) {
				continue
			}
			if f, _ := g.finding(v); f != nil {
				p.report.Violations = append(p.report.Violations, *f)
			}
		}
	}
	p.report.finish()
	sort.Strings(ids)
	return p.report, ids
}

// skips reports whether ss holds a skip of file.
func skips(ss []Skip, file string) bool {
	for _, s := range ss {
		if s.File == file {
			return true
		}
	}
	return false
}

// maxPerFile caps the violations reported for one rule in one file. A
// pattern that matches every line would otherwise bury everything else.
const maxPerFile = 100

// finish sorts the findings, keeps the first maxPerFile of each rule in each
// file, and sorts the skips, each file once per reason.
func (r *Report) finish() {
	sortFindings(r.Violations)
	type key struct{ rule, file string }
	n := map[key]int{}
	kept := r.Violations[:0]
	for _, f := range r.Violations {
		k := key{f.RuleID, f.File}
		if n[k]++; n[k] > maxPerFile {
			r.Truncated++
			continue
		}
		kept = append(kept, f)
	}
	r.Violations = kept

	sort.SliceStable(r.Skipped, func(i, j int) bool { return r.Skipped[i].File < r.Skipped[j].File })
	skipped := r.Skipped[:0]
	for i, s := range r.Skipped {
		if i == 0 || s != r.Skipped[i-1] {
			skipped = append(skipped, s)
		}
	}
	r.Skipped = skipped
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
		msg = Summary(r.Body)
	}
	return &Finding{RuleID: r.ID, Excerpt: r.excerpt, File: v.File, Line: v.Line, Column: v.Column, Message: msg, Blocking: r.blocking}, true
}

type prepared struct {
	groups []group
	report Report
}

// prepare groups idx's checked rules by checker and validates them. Invalid
// rules become problems and are left out; the others still run, so one
// stele check shows every problem. With content, only ContentCheckers are
// asked to validate, and only their rules are grouped. An error is a checker
// that could not validate.
func prepare(ctx context.Context, idx index.Index, cs map[string]Checker, content bool) (prepared, error) {
	p := prepared{report: NewReport(idx.Project)}
	problem := func(r rule, msg string) {
		p.report.Problems = append(p.report.Problems, Problem{RuleID: r.ID, Excerpt: r.excerpt, Message: msg})
	}

	byChecker := map[string][]rule{}
	for _, e := range idx.Rules {
		r := rule{Rule: Rule{ID: e.ID, Body: e.Body, Scope: e.Scope, Meta: e.Meta}, blocking: true, excerpt: Excerpt(e.Body)}
		name, ok := e.Meta["check"]
		if !ok {
			// Guidance only. The regex checker's own keys with no check
			// mean a check was meant and forgotten (#148). Other keys are
			// kept without a word (D46): a source may add its own.
			if msg := unread(e.Meta); msg != "" {
				problem(r, msg)
			}
			continue
		}
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
		if _, ok := g.checker.(ContentChecker); content && !ok {
			continue
		}
		plain := make([]Rule, len(rules))
		for i, r := range rules {
			plain[i] = r.Rule
		}
		problems, err := g.checker.Validate(ctx, plain)
		if err != nil {
			return prepared{report: p.report}, checkerErr(name, err)
		}
		bad := map[string]bool{}
		for _, pr := range problems {
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
	sortProblems(p.report.Problems, idx)
	return p, nil
}

// sortProblems puts problems in the order of their rules in idx, the order
// they are written in, whichever step found them. A problem naming no rule of
// idx goes last.
func sortProblems(ps []Problem, idx index.Index) {
	at := make(map[string]int, len(idx.Rules))
	for i, e := range idx.Rules {
		at[e.ID] = i
	}
	pos := func(id string) int {
		if i, ok := at[id]; ok {
			return i
		}
		return len(idx.Rules)
	}
	sort.SliceStable(ps, func(i, j int) bool { return pos(ps[i].RuleID) < pos(ps[j].RuleID) })
}

// unread says which keys of the built-in regex checker a rule with no check
// has, which nothing reads, or "" for none.
func unread(meta map[string]string) string {
	var keys []string
	for _, k := range []string{"message", "pattern"} {
		if _, ok := meta[k]; ok {
			keys = append(keys, k)
		}
	}
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%s without check: the rule is guidance only and nothing checks %[1]s; add check: regex or remove %[1]s", keys[0])
	}
	return fmt.Sprintf("%s without check: the rule is guidance only and nothing reads them; add check: regex or remove them", strings.Join(keys, " and "))
}

// Notes are what is worth saying about idx's rules that does not make them
// invalid: blocking on a rule with no check, which does nothing, and a key
// that looks like a typo of one of stele's own (D99), such as scopes. Each
// note is a Problem the caller shows, not one that fails anything.
func Notes(idx index.Index) []Problem {
	var notes []Problem
	for _, e := range idx.Rules {
		_, checked := e.Meta["check"]
		if _, ok := e.Meta["blocking"]; ok && !checked {
			notes = append(notes, Problem{RuleID: e.ID, Excerpt: Excerpt(e.Body), Message: "blocking without check: the rule is guidance only, so blocking does nothing"})
		}
		keys := make([]string, 0, len(e.Meta))
		for k := range e.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if near := typoOf(k); near != "" {
				notes = append(notes, Problem{RuleID: e.ID, Excerpt: Excerpt(e.Body), Message: fmt.Sprintf("unknown key %s, did you mean %s?", k, near)})
			}
		}
	}
	return notes
}

// ownKeys are the rule keys stele itself defines: stele.md's fields (spec
// §3), the keys stele reads (D46) and the built-in regex checker's.
var ownKeys = []string{"scope", "check", "pattern", "message", "blocking", "tags", "description"}

// pluginKeys are the keys of the checker plugins that ship with stele (D90).
// They are never taken for a typo, however close to one of ownKeys.
var pluginKeys = map[string]bool{"rules": true, "ids": true, "scanners": true, "severity": true}

// typoOf returns the key of ownKeys that key is probably a typo of, or "":
// one edit away (a letter added, removed, changed, or two swapped), or two
// for one of seven letters or more (pattern, message, blocking,
// description), so score or slope is no typo of scope. A key equal to one
// of them, or that a shipped checker plugin reads, is no typo. Other keys are a checker's or a
// source's, and stay without a word (D46, D93).
func typoOf(key string) string {
	if pluginKeys[key] {
		return ""
	}
	best, bestD := "", 3
	for _, own := range ownKeys {
		if own == key {
			return ""
		}
		limit := 2
		if len(own) < 7 {
			limit = 1
		}
		if d := editDistance(key, own); d <= limit && d < bestD {
			best, bestD = own, d
		}
	}
	return best
}

// editDistance is the optimal string alignment distance between a and b:
// insertions, deletions, substitutions and swaps of two adjacent bytes.
// Keys are ASCII.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
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
		return "the only one is the built-in " + names[0] + `; list an external checker under "checkers" in stele.json`
	}
	return "the checkers are " + strings.Join(names, ", ") + `; list an external checker under "checkers" in stele.json`
}

// ownFiles are stele's own files. A rule is never checked against them: a
// pattern quoted in stele.md would otherwise flag stele.md itself. The files
// compile generates for an agent, such as the rules file it loads (#68),
// which holds every rule's text, are skipped by the caller, which knows them.
var ownFiles = []string{"/stele.md", "/stele.json", "/stele.lock", "/" + index.Dir + "/"}

// Own reports whether file, relative to the root, is one of stele's own.
func Own(file string) bool {
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

// excerptLen bounds an excerpt, and summaryLen a summary, in runes.
const (
	excerptLen = 60
	summaryLen = 120
)

// Excerpt is the short text shown beside a rule id: a UUID cannot be read
// (spec §12). It is the body's first paragraph, with its whitespace
// collapsed, cut at a word to about 60 characters.
func Excerpt(body string) string {
	return shorten(paragraph(body), excerptLen)
}

// Summary is what a violation of a rule with no message says: the start of
// the rule (spec §3), its first paragraph cut at a word to about 120
// characters, enough for a sentence or two.
func Summary(body string) string {
	return shorten(paragraph(body), summaryLen)
}

// paragraph is body's first paragraph, its whitespace collapsed.
func paragraph(body string) string {
	var words []string
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 && len(words) > 0 {
			break // the paragraph ended
		}
		words = append(words, f...)
	}
	return strings.Join(words, " ")
}

// shorten cuts s to at most n runes and marks the cut with "…". It cuts
// between words, unless that would lose more than half of s's first n runes:
// one long word is cut inside.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := r[:n]
	if r[n] != ' ' {
		for i := n - 1; i > n/2; i-- {
			if cut[i] == ' ' {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRight(string(cut), " ,;:") + "…"
}

// Shown is how to show a violation beside its rule id: the text to quote,
// and the message to add after it, "" when the quote says it all. A message
// that has a Summary's shape, at most about 120 characters with the excerpt
// as its start, is quoted alone, in place of the excerpt. Any other message,
// such as a long one of the rule's own, follows the excerpt, where its cap
// applies.
func Shown(excerpt, message string) (quote, rest string) {
	switch {
	case message == "" || message == excerpt:
		return excerpt, ""
	case utf8.RuneCountInString(message) <= summaryLen+1 && shorten(message, excerptLen) == excerpt:
		return message, ""
	}
	return excerpt, message
}

// checkerErr is err from the checker name, prefixed with it unless it names
// its plugin already (source.SelfNamed).
func checkerErr(name string, err error) error {
	var sn source.SelfNamed
	if errors.As(err, &sn) {
		return err
	}
	return fmt.Errorf("%s: %w", name, err)
}
