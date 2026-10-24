package check

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
)

// fake is a checker that records what it was given and returns canned
// violations.
type fake struct {
	name     string
	problems []Problem
	found    []Violation
	skipped  []Skip
	err      error

	gotRules []string
	gotFiles []string
}

func (f *fake) Name() string { return f.name }

func (f *fake) Validate(rules []Rule) []Problem { return f.problems }

func (f *fake) Check(_ context.Context, _ string, rules []Rule, files []string) (Result, error) {
	for _, r := range rules {
		f.gotRules = append(f.gotRules, r.ID)
	}
	f.gotFiles = files
	return Result{Violations: f.found, Skipped: f.skipped}, f.err
}

// contentFake is a fake that can also check content it is handed.
type contentFake struct {
	fake
	gotContent string
}

func (f *contentFake) CheckContent(rules []Rule, file string, content []byte) Result {
	for _, r := range rules {
		f.gotRules = append(f.gotRules, r.ID)
	}
	f.gotFiles = []string{file}
	f.gotContent = string(content)
	return Result{Violations: f.found, Skipped: f.skipped}
}

func TestRunSkipped(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{entry("a", "A.", nil, map[string]string{"check": "fake"})}}
	f := &fake{name: "fake", skipped: []Skip{{File: "z.bin", Reason: "binary"}, {File: "big.txt", Reason: "larger than 32 MiB"}}}
	r, err := Run(context.Background(), "root", idx, []string{"big.txt", "z.bin"}, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	want := []Skip{{File: "big.txt", Reason: "larger than 32 MiB"}, {File: "z.bin", Reason: "binary"}}
	if diff := cmp.Diff(want, r.Skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
	if r.ExitCode() != 0 {
		t.Errorf("ExitCode = %d, want skips not to change it", r.ExitCode())
	}

	cf := &contentFake{fake: fake{name: "fake", skipped: []Skip{{File: "z.bin", Reason: "binary"}}}}
	rf := RunFile(idx, "z.bin", []byte("x"), map[string]Checker{"fake": cf})
	if diff := cmp.Diff([]Skip{{File: "z.bin", Reason: "binary"}}, rf.Skipped); diff != "" {
		t.Errorf("RunFile skipped (-want +got):\n%s", diff)
	}
}

func TestRunTruncates(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("a", "A.", nil, map[string]string{"check": "fake"}),
		entry("b", "B.", nil, map[string]string{"check": "fake", "blocking": "false"}),
	}}
	var found []Violation
	for i := 1; i <= maxPerFile+5; i++ {
		found = append(found, Violation{RuleID: "a", File: "x.go", Line: i, Column: 1})
	}
	found = append(found, Violation{RuleID: "a", File: "y.go", Line: 1, Column: 1}) // another file: its own cap
	for i := 1; i <= maxPerFile+2; i++ {
		found = append(found, Violation{RuleID: "b", File: "x.go", Line: i, Column: 2})
	}
	f := &fake{name: "fake", found: found}
	r, err := Run(context.Background(), "root", idx, []string{"x.go", "y.go"}, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 2*maxPerFile+1 || r.Truncated != 7 {
		t.Errorf("violations %d, truncated %d, want %d and 7", len(r.Violations), r.Truncated, 2*maxPerFile+1)
	}
	for _, f := range r.Violations {
		if f.Line > maxPerFile {
			t.Errorf("kept %+v, want only the first %d matches of each rule kept", f, maxPerFile)
		}
	}
}

func entry(id, body string, scope []string, meta map[string]string) index.Entry {
	return index.Entry{ID: id, Source: "stelemd", Body: body, Scope: scope, Meta: meta}
}

func TestRunGroupsRulesByChecker(t *testing.T) {
	idx := index.Index{Project: "p", Rules: []index.Entry{
		entry("a", "Rule a.", nil, map[string]string{"check": "fake"}),
		entry("b", "Guidance only.", nil, nil),
		entry("c", "Rule c.", nil, map[string]string{"check": "other", "pattern": "x"}),
		entry("d", "Rule d.", nil, map[string]string{"check": "fake"}),
	}}
	f, o := &fake{name: "fake"}, &fake{name: "other"}
	r, err := Run(context.Background(), "root", idx, []string{"x.go"}, map[string]Checker{"fake": f, "other": o})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a", "d"}, f.gotRules); diff != "" {
		t.Errorf("fake got rules (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"c"}, o.gotRules); diff != "" {
		t.Errorf("other got rules (-want +got):\n%s", diff)
	}
	if r.V != 1 || r.Project != "p" || len(r.Problems) != 0 || len(r.Violations) != 0 {
		t.Errorf("report = %+v, want v 1, project p, nothing found", r)
	}
}

func TestValidate(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("unknown", "Use semgrep.", nil, map[string]string{"check": "semgrep"}),
		entry("empty", "Empty check.", nil, map[string]string{"check": " "}),
		entry("yes", "Blocking yes.", nil, map[string]string{"check": "fake", "blocking": "yes"}),
		entry("bad", "Fails validation.", nil, map[string]string{"check": "fake"}),
		entry("good", "Still runs.", nil, map[string]string{"check": "fake"}),
		entry("guide", "Guidance only.", nil, nil),
	}}
	f := &fake{name: "fake", problems: []Problem{{RuleID: "bad", Message: "fake: no pattern"}}}
	got := Validate(idx, map[string]Checker{"fake": f})
	r, err := Run(context.Background(), "root", idx, nil, map[string]Checker{"fake": &fake{name: "fake", problems: f.problems}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(r.Problems, got); diff != "" {
		t.Errorf("Validate differs from Run's problems (-run +validate):\n%s", diff)
	}
	if len(got) != 4 {
		t.Errorf("Validate = %d problems, want 4", len(got))
	}
	if f.gotRules != nil || f.gotFiles != nil {
		t.Errorf("Validate ran the checker on rules %v, files %v; it must read no file", f.gotRules, f.gotFiles)
	}
	if ps := Validate(index.Index{}, map[string]Checker{"fake": f}); len(ps) != 0 {
		t.Errorf("Validate(empty index) = %v, want none", ps)
	}
}

func TestRunRuleProblems(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("unknown", "Use semgrep.", nil, map[string]string{"check": "semgrep"}),
		entry("empty", "Empty check.", nil, map[string]string{"check": " "}),
		entry("yes", "Blocking yes.", nil, map[string]string{"check": "fake", "blocking": "yes"}),
		entry("bad", "Fails validation.", nil, map[string]string{"check": "fake"}),
		entry("good", "Still runs.", nil, map[string]string{"check": "fake"}),
	}}
	f := &fake{name: "fake", problems: []Problem{{RuleID: "bad", Message: "fake: no pattern"}}}
	r, err := Run(context.Background(), "root", idx, []string{"x.go"}, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"good"}, f.gotRules); diff != "" {
		t.Errorf("checked rules (-want +got):\n%s", diff)
	}
	want := []Problem{
		{RuleID: "unknown", Excerpt: "Use semgrep.", Message: `check: "semgrep" is not a checker; the only one is the built-in fake`},
		{RuleID: "empty", Excerpt: "Empty check.", Message: "check is empty"},
		{RuleID: "yes", Excerpt: "Blocking yes.", Message: `blocking: "yes" is not true or false`},
		{RuleID: "bad", Excerpt: "Fails validation.", Message: "fake: no pattern"},
	}
	if diff := cmp.Diff(want, r.Problems); diff != "" {
		t.Errorf("problems (-want +got):\n%s", diff)
	}
	if r.ExitCode() != 1 {
		t.Errorf("ExitCode = %d, want 1 for an invalid rule", r.ExitCode())
	}
}

func TestRunScope(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("go", "Go only.", []string{"**/*.go"}, map[string]string{"check": "fake"}),
	}}
	f := &fake{name: "fake", found: []Violation{
		{RuleID: "go", File: "a.go", Line: 1, Column: 1},
		{RuleID: "go", File: "b.md", Line: 1, Column: 1}, // out of scope: dropped
	}}
	files := []string{"a.go", "b.md", "stele.md", ".stele/index.json", "stele.lock", "stele.json"}
	r, err := Run(context.Background(), "root", idx, files, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a.go"}, f.gotFiles); diff != "" {
		t.Errorf("checked files (-want +got):\n%s", diff)
	}
	if len(r.Violations) != 1 || r.Violations[0].File != "a.go" {
		t.Errorf("violations = %+v, want only a.go", r.Violations)
	}

	// An unscoped rule gets every file but stele's own.
	idx.Rules[0].Scope = nil
	f.gotFiles = nil
	if _, err := Run(context.Background(), "root", idx, files, map[string]Checker{"fake": f}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a.go", "b.md"}, f.gotFiles); diff != "" {
		t.Errorf("unscoped rule files (-want +got):\n%s", diff)
	}
}

func TestRunScopeExclusion(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("go", "Go, not tests.", []string{"**/*.go", "!**/*_test.go"}, map[string]string{"check": "fake"}),
	}}
	f := &fake{name: "fake", found: []Violation{
		{RuleID: "go", File: "a.go", Line: 1, Column: 1},
		{RuleID: "go", File: "a_test.go", Line: 1, Column: 1}, // excluded: dropped
	}}
	r, err := Run(context.Background(), "root", idx, []string{"a.go", "a_test.go"}, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a.go"}, f.gotFiles); diff != "" {
		t.Errorf("checked files (-want +got):\n%s", diff)
	}
	if len(r.Violations) != 1 || r.Violations[0].File != "a.go" {
		t.Errorf("violations = %+v, want only a.go", r.Violations)
	}
}

func TestRunNoFilesNoCall(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("go", "Go only.", []string{"*.go"}, map[string]string{"check": "fake"}),
	}}
	f := &fake{name: "fake", err: errors.New("must not be called")}
	if _, err := Run(context.Background(), "root", idx, []string{"a.md"}, map[string]Checker{"fake": f}); err != nil {
		t.Errorf("Run = %v, want the checker skipped with no files", err)
	}
}

func TestRunFindings(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{
		entry("block", "Never build SQL\n  by   string concatenation.", nil, map[string]string{"check": "fake"}),
		entry("warn", "Log through the shared logger.", nil, map[string]string{"check": "fake", "blocking": "false"}),
		entry("msg", "Has a message.", nil, map[string]string{"check": "fake", "blocking": "true"}),
	}}
	f := &fake{name: "fake", found: []Violation{
		{RuleID: "warn", File: "b.go", Line: 3, Column: 1},
		{RuleID: "block", File: "b.go", Line: 1, Column: 5},
		{RuleID: "msg", File: "a.go", Line: 9, Column: 2, Message: "use the pool"},
	}}
	r, err := Run(context.Background(), "root", idx, []string{"a.go", "b.go"}, map[string]Checker{"fake": f})
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{
		{RuleID: "msg", Excerpt: "Has a message.", File: "a.go", Line: 9, Column: 2, Message: "use the pool", Blocking: true},
		{RuleID: "block", Excerpt: "Never build SQL by string concatenation.", File: "b.go", Line: 1, Column: 5, Message: "Never build SQL by string concatenation.", Blocking: true},
		{RuleID: "warn", Excerpt: "Log through the shared logger.", File: "b.go", Line: 3, Column: 1, Message: "Log through the shared logger.", Blocking: false},
	}
	if diff := cmp.Diff(want, r.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
}

func TestRunErrors(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{entry("a", "A.", nil, map[string]string{"check": "fake"})}}

	f := &fake{name: "fake", err: errors.New("boom")}
	if _, err := Run(context.Background(), "root", idx, []string{"x"}, map[string]Checker{"fake": f}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("checker error: Run = %v, want boom", err)
	}

	f = &fake{name: "fake", found: []Violation{{RuleID: "nope", File: "x", Line: 1, Column: 1}}}
	if _, err := Run(context.Background(), "root", idx, []string{"x"}, map[string]Checker{"fake": f}); err == nil {
		t.Error("violation of an unknown rule: Run = nil, want an error")
	}
}

func TestRunFile(t *testing.T) {
	idx := index.Index{Project: "p", Rules: []index.Entry{
		entry("go", "Go only.", []string{"*.go"}, map[string]string{"check": "fake"}),
		entry("plug", "A plugin rule.", nil, map[string]string{"check": "plugin"}),
	}}
	f := &contentFake{fake: fake{name: "fake", found: []Violation{
		{RuleID: "go", File: "a.go", Line: 2, Column: 3},
		{RuleID: "ghost", File: "a.go", Line: 1, Column: 1}, // unknown rule: dropped
	}}}
	p := &fake{name: "plugin", err: errors.New("a plugin must not run here")}
	r := RunFile(idx, "a.go", []byte("content"), map[string]Checker{"fake": f, "plugin": p})
	if f.gotContent != "content" {
		t.Errorf("content = %q, want it handed to the checker", f.gotContent)
	}
	if p.gotRules != nil {
		t.Error("a checker that cannot check content was run")
	}
	want := []Finding{{RuleID: "go", Excerpt: "Go only.", File: "a.go", Line: 2, Column: 3, Message: "Go only.", Blocking: true}}
	if diff := cmp.Diff(want, r.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
	if r.Project != "p" || len(r.Problems) != 0 {
		t.Errorf("report = %+v", r)
	}

	// stele's own files are never checked.
	f.gotRules = nil
	RunFile(idx, "stele.md", []byte("x"), map[string]Checker{"fake": f})
	if f.gotRules != nil {
		t.Error("stele.md was checked against its own rules")
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want int
	}{
		{"clean", Report{}, 0},
		{"warnings only", Report{Violations: []Finding{{Blocking: false}}}, 0},
		{"blocking", Report{Violations: []Finding{{Blocking: false}, {Blocking: true}}}, 1},
		{"problem", Report{Problems: []Problem{{Message: "x"}}}, 1},
	}
	for _, tt := range tests {
		if got := tt.r.ExitCode(); got != tt.want {
			t.Errorf("%s: ExitCode = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestExcerpt(t *testing.T) {
	tests := []struct{ body, want string }{
		{"Never build SQL.", "Never build SQL."},
		{"\n\n  First   line\twith\n  spaces.\n\nSecond paragraph.", "First line with spaces."},
		{strings.Repeat("é", 70), strings.Repeat("é", 60) + "…"},
		{strings.Repeat("a", 60), strings.Repeat("a", 60)},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Excerpt(tt.body); got != tt.want {
			t.Errorf("Excerpt(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestReportJSON(t *testing.T) {
	r, err := Run(context.Background(), "root", index.Index{Project: "p"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"v":1,"project":"p","problems":[],"violations":[],"skipped":[],"truncated":0}`; string(data) != want {
		t.Errorf("empty report = %s, want %s", data, want)
	}

	f := Finding{RuleID: "a", Excerpt: "A.", File: "x.go", Line: 1, Column: 2, Message: "m", Blocking: true}
	data, _ = json.Marshal(f)
	if want := `{"rule_id":"a","excerpt":"A.","file":"x.go","line":1,"column":2,"message":"m","blocking":true}`; string(data) != want {
		t.Errorf("finding = %s, want %s", data, want)
	}
	data, _ = json.Marshal(Skip{File: "a.bin", Reason: "binary"})
	if want := `{"file":"a.bin","reason":"binary"}`; string(data) != want {
		t.Errorf("skip = %s, want %s", data, want)
	}
	data, _ = json.Marshal(Problem{Message: "stele.md:1: rule has no body"})
	if want := `{"message":"stele.md:1: rule has no body"}`; string(data) != want {
		t.Errorf("root problem = %s, want %s", data, want)
	}
}
