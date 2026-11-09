package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/printable"
)

// ErrCheckers is a "checkers" key in stele.json stele cannot use.
var ErrCheckers = errors.New("bad checkers in stele.json")

// What a checker's answers may hold (D78).
const (
	maxViolations   = 100000
	maxMessage      = 1024
	validateTimeout = 10 * time.Second
)

// Checkers is every checker stele check may run under root: the built-ins,
// then each external one stele.json lists under "checkers", found on PATH
// with run, so a missing one fails before any file is read, even one no rule
// names. Nothing is started here. Only stele check calls it (D79).
func Checkers(root string, run Runner) (map[string]check.Checker, error) {
	entries, err := listed(root, "checkers", ErrCheckers)
	if err != nil {
		return nil, err
	}
	cs := BuiltinCheckers()
	seen := map[string]int{}
	for i, e := range entries {
		p, err := external(KindChecker, "checkers", e, CheckTimeout, run)
		if err != nil {
			var pe *Error
			if errors.As(err, &pe) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: checkers[%d]: %v", ErrCheckers, i, err)
		}
		if j, ok := seen[e.Name]; ok {
			return nil, fmt.Errorf("%w: checkers[%d]: %q is listed twice, as checkers[%d]", ErrCheckers, i, e.Name, j)
		}
		seen[e.Name] = i
		cs[e.Name] = &execChecker{name: p.name, bin: p.bin, root: root, config: p.config, timeout: p.timeout, run: run}
	}
	return cs, nil
}

// NamedCheckers is the built-in checkers and, by name only, each external
// one stele.json lists: what stele compile and the hook validate rules
// against, so a rule naming a listed checker is valid there. A named checker
// is not run and starts nothing; a bad entry is left out, and a bad
// stele.json leaves the built-ins. stele check reports either.
func NamedCheckers(root string) map[string]check.Checker {
	cs := BuiltinCheckers()
	entries, err := listed(root, "checkers", ErrCheckers)
	if err != nil {
		return cs
	}
	for _, e := range entries {
		if ValidName(e.Name) == nil {
			cs[e.Name] = named{name: e.Name}
		}
	}
	return cs
}

// named is an external checker known by its name only.
type named struct{ name string }

func (n named) Name() string { return n.name }

func (n named) Validate(context.Context, []check.Rule) ([]check.Problem, error) { return nil, nil }

func (n named) Check(context.Context, string, []check.Rule, []string) (check.Result, error) {
	return check.Result{}, fmt.Errorf("external checker %s runs only in stele check", n.name)
}

// execChecker is an external checker: a stele-checker-<name> on PATH. To
// stele check it is a checker like regex. It is not a ContentChecker, so the
// hook never runs it.
type execChecker struct {
	name, bin string
	root      string // the project, sent as project_dir (ADR-0016)
	config    json.RawMessage
	timeout   time.Duration // check's
	run       Runner

	once sync.Once
	derr error
}

func (c *execChecker) Name() string { return c.name }

func (c *execChecker) fail(op string, err error) error {
	return &Error{Kind: KindChecker, Name: c.name, List: "checkers", Exe: Exe(KindChecker, c.name), Op: op, Err: err}
}

func (c *execChecker) call(ctx context.Context, op string, fields map[string]any, max int, timeout time.Duration, v any) error {
	c.once.Do(func() { _, c.derr = describe(ctx, c.run, KindChecker, c.name, c.bin) })
	if c.derr != nil {
		return c.derr
	}
	req, err := Request(op, fields)
	if err != nil {
		return c.fail(op, err)
	}
	out, err := c.run.Run(ctx, c.bin, req, max, timeout)
	if err != nil {
		return c.fail(op, err)
	}
	if !utf8.Valid(out) {
		return c.fail(op, fmt.Errorf("%w: answer is not valid UTF-8", ErrProtocol))
	}
	if err := DecodeAnswer(out, v); err != nil {
		return c.fail(op, err)
	}
	return nil
}

// sentCheckRule is a rule as stele sends it to a checker.
type sentCheckRule struct {
	ID    string            `json:"id"`
	Body  string            `json:"body"`
	Scope []string          `json:"scope"`
	Meta  map[string]string `json:"meta"`
}

func sendRules(rules []check.Rule) ([]sentCheckRule, map[string]bool) {
	out := make([]sentCheckRule, len(rules))
	ids := make(map[string]bool, len(rules))
	for i, r := range rules {
		out[i] = sentCheckRule{ID: r.ID, Body: r.Body, Scope: r.Scope, Meta: r.Meta}
		ids[r.ID] = true
	}
	return out, ids
}

func (c *execChecker) Validate(ctx context.Context, rules []check.Rule) ([]check.Problem, error) {
	sent, ids := sendRules(rules)
	var a struct {
		Problems *[]struct {
			RuleID  string `json:"rule_id"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	if err := c.call(ctx, "validate", map[string]any{"project_dir": c.root, "config": c.config, "rules": sent}, SmallAnswer, validateTimeout, &a); err != nil {
		return nil, err
	}
	if a.Problems == nil {
		return nil, c.fail("validate", fmt.Errorf("%w: answer has no problems", ErrProtocol))
	}
	var ps []check.Problem
	for _, p := range *a.Problems {
		if !ids[p.RuleID] {
			return nil, c.fail("validate", fmt.Errorf("%w: a problem with %q, which is not one of the rules sent", ErrProtocol, printable.Clean(p.RuleID)))
		}
		ps = append(ps, check.Problem{RuleID: p.RuleID, Message: line(p.Message)})
	}
	return ps, nil
}

func (c *execChecker) Check(ctx context.Context, root string, rules []check.Rule, files []string) (check.Result, error) {
	sent, ids := sendRules(rules)
	var a struct {
		Violations *[]struct {
			RuleID  string `json:"rule_id"`
			File    string `json:"file"`
			Line    int    `json:"line"`
			Column  int    `json:"column"`
			Message string `json:"message"`
		} `json:"violations"`
		Skipped []struct {
			File   string `json:"file"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	// root is project_dir's old name, kept for plugins that read it (ADR-0016).
	fields := map[string]any{"project_dir": root, "root": root, "config": c.config, "files": files, "rules": sent}
	if err := c.call(ctx, "check", fields, LargeAnswer, c.timeout, &a); err != nil {
		return check.Result{}, err
	}
	bad := func(format string, args ...any) (check.Result, error) {
		return check.Result{}, c.fail("check", fmt.Errorf("%w: %s", ErrProtocol, fmt.Sprintf(format, args...)))
	}
	if a.Violations == nil {
		return bad("answer has no violations")
	}
	if len(*a.Violations) > maxViolations {
		return bad("%d violations, at most %d", len(*a.Violations), maxViolations)
	}
	sentFile := make(map[string]bool, len(files))
	for _, f := range files {
		sentFile[f] = true
	}
	var res check.Result
	for _, v := range *a.Violations {
		switch {
		case !ids[v.RuleID]:
			return bad("a violation of %q, which is not one of the rules sent", printable.Clean(v.RuleID))
		case !sentFile[v.File]:
			return bad("a violation in %q, which is not one of the files sent", printable.Clean(v.File))
		case v.Line < 1 || v.Column < 1:
			return bad("a violation at line %d, column %d: both start at 1", v.Line, v.Column)
		}
		res.Violations = append(res.Violations, check.Violation{RuleID: v.RuleID, File: v.File, Line: v.Line, Column: v.Column, Message: line(v.Message)})
	}
	for _, s := range a.Skipped {
		if !sentFile[s.File] {
			return bad("a skip of %q, which is not one of the files sent", printable.Clean(s.File))
		}
		res.Skipped = append(res.Skipped, check.Skip{File: s.File, Reason: line(s.Reason)})
	}
	return res, nil
}

// line is s made one printable line of at most maxMessage bytes.
func line(s string) string { return cut(printable.Clean(s), maxMessage) }
