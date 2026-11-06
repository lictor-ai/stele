package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/check/regex"
)

const describeChecker = `{"protocol":1,"kind":"checker","protocols":[1],"version":"2.0.0"}`

func TestCheckersBuiltinAndExternal(t *testing.T) {
	root := steleJSON(t, `{"checkers": [{"name": "semgrep", "config": {"rules": "p/go"}, "timeout": "10m"}]}`)
	run := &fakeRunner{}
	cs, err := Checkers(root, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cs[regex.Name]; !ok {
		t.Error("no built-in regex")
	}
	c, ok := cs["semgrep"].(*execChecker)
	if !ok || c.Name() != "semgrep" || c.timeout.Minutes() != 10 || string(c.config) != `{"rules": "p/go"}` {
		t.Fatalf("semgrep = %+v", cs["semgrep"])
	}
	if _, ok := cs["semgrep"].(check.ContentChecker); ok {
		t.Error("an external checker is a ContentChecker: the hook would run it")
	}
	if len(run.got) != 0 {
		t.Errorf("building the checkers ran %v", run.got)
	}
}

func TestCheckersRefusals(t *testing.T) {
	for name, content := range map[string]string{
		"built in": `{"checkers": [{"name": "regex"}]}`,
		"bad name": `{"checkers": [{"name": "x/y"}]}`,
		"twice":    `{"checkers": [{"name": "a"}, {"name": "a"}]}`,
		"key":      `{"checkers": [{"name": "a", "conf": {}}]}`,
		"timeout":  `{"checkers": [{"name": "a", "timeout": "1h"}]}`,
		"not list": `{"checkers": {"name": "a"}}`,
		"too many": `{"checkers": [` + strings.TrimSuffix(strings.Repeat(`{"name": "a"},`, 17), ",") + `]}`,
	} {
		if _, err := Checkers(steleJSON(t, content), &fakeRunner{}); err == nil || !strings.Contains(err.Error(), "stele.json") {
			t.Errorf("%s: Checkers = %v, want an error naming stele.json", name, err)
		}
	}
}

func TestCheckersNotFound(t *testing.T) {
	_, err := Checkers(steleJSON(t, `{"checkers": [{"name": "semgrep"}]}`), &fakeRunner{findErr: ErrPluginNotFound})
	want := `checker "semgrep": stele-checker-semgrep not found on PATH: install it, or remove "semgrep" from "checkers" in stele.json`
	if err == nil || err.Error() != want {
		t.Errorf("Checkers = %v, want %s", err, want)
	}
}

func TestNamedCheckersStartsNothing(t *testing.T) {
	root := steleJSON(t, `{"checkers": [{"name": "semgrep"}, {"name": "Bad.Name"}, {"name": "regex"}]}`)
	cs := NamedCheckers(root)
	if _, ok := cs[regex.Name].(check.ContentChecker); !ok {
		t.Error("regex is not the built-in")
	}
	c, ok := cs["semgrep"]
	if !ok {
		t.Fatal("no semgrep")
	}
	if _, ok := c.(check.ContentChecker); ok {
		t.Error("the named checker is a ContentChecker")
	}
	if ps, err := c.Validate(context.Background(), []check.Rule{{ID: "r"}}); err != nil || len(ps) != 0 {
		t.Errorf("Validate = %v, %v; want nothing", ps, err)
	}
	if _, err := c.Check(context.Background(), root, nil, []string{"a.go"}); err == nil {
		t.Error("the named checker checked")
	}
	if _, ok := cs["Bad.Name"]; ok {
		t.Error("a bad name was kept")
	}
	if len(NamedCheckers(steleJSON(t, `{`))) != 1 {
		t.Error("a bad stele.json lost the builtins")
	}
}

func newExecChecker(run Runner) *execChecker {
	return &execChecker{name: "semgrep", bin: "/bin/stele-checker-semgrep", config: []byte(`{}`), timeout: CheckTimeout, run: run}
}

var checkRules = []check.Rule{{ID: "r1", Body: "No eval.", Scope: []string{"**/*.js"}, Meta: map[string]string{"check": "semgrep", "rule": "no-eval"}}}

func TestExecCheckerValidate(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": describeChecker, "validate": `{"protocol":1,"problems":[{"rule_id":"r1","message":"no rule \u001b[1mno-eval"}]}`}}
	ps, err := newExecChecker(run).Validate(context.Background(), checkRules)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].RuleID != "r1" || ps[0].Message != "no rule no-eval" {
		t.Errorf("problems = %+v", ps)
	}
	if !strings.Contains(run.got[1], `"rules":[{"id":"r1","body":"No eval.","scope":["**/*.js"],"meta":{"check":"semgrep","rule":"no-eval"}}]`) {
		t.Errorf("request = %s", run.got[1])
	}
	run = &fakeRunner{answers: map[string]string{"describe": describeChecker, "validate": `{"protocol":1,"problems":[{"rule_id":"other","message":"x"}]}`}}
	if _, err := newExecChecker(run).Validate(context.Background(), checkRules); !errors.Is(err, ErrProtocol) {
		t.Errorf("a problem with a rule not sent: %v, want ErrProtocol", err)
	}
}

func TestExecCheckerCheck(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": describeChecker, "check": `{"protocol":1,
		"violations":[{"rule_id":"r1","file":"web/a.js","line":3,"column":7,"message":"eval\nhere"}],
		"skipped":[{"file":"web/b.js","reason":"too big"}]}`}}
	res, err := newExecChecker(run).Check(context.Background(), "/repo", checkRules, []string{"web/a.js", "web/b.js"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 1 || res.Violations[0] != (check.Violation{RuleID: "r1", File: "web/a.js", Line: 3, Column: 7, Message: "eval here"}) {
		t.Errorf("violations = %+v", res.Violations)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != (check.Skip{File: "web/b.js", Reason: "too big"}) {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if !strings.Contains(run.got[1], `"files":["web/a.js","web/b.js"]`) || !strings.Contains(run.got[1], `"root":"/repo"`) {
		t.Errorf("request = %s", run.got[1])
	}
}

func TestExecCheckerRejectsAnswers(t *testing.T) {
	for name, ans := range map[string]string{
		"file not sent": `{"protocol":1,"violations":[{"rule_id":"r1","file":"../etc/x","line":1,"column":1}]}`,
		"line 0":        `{"protocol":1,"violations":[{"rule_id":"r1","file":"web/a.js","line":0,"column":1}]}`,
		"column 0":      `{"protocol":1,"violations":[{"rule_id":"r1","file":"web/a.js","line":1,"column":0}]}`,
		"skip not sent": `{"protocol":1,"violations":[],"skipped":[{"file":"x.js","reason":"r"}]}`,
		"no violations": `{"protocol":1}`,
		"error":         `{"protocol":1,"error":"semgrep is not installed"}`,
	} {
		run := &fakeRunner{answers: map[string]string{"describe": describeChecker, "check": ans}}
		_, err := newExecChecker(run).Check(context.Background(), "/repo", checkRules, []string{"web/a.js"})
		if err == nil || !strings.HasPrefix(err.Error(), "stele-checker-semgrep check: ") {
			t.Errorf("%s: Check = %v, want an error naming the checker", name, err)
		}
	}
}
