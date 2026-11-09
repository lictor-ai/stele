package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lictor-ai/stele/internal/extcheck"
	"github.com/lictor-ai/stele/internal/extcheck/extchecktest"
	"github.com/lictor-ai/stele/internal/plugin"
)

// TestMain makes this test binary a fake opengrep when it is started under
// that name (extchecktest). The real Opengrep never runs here.
func TestMain(m *testing.M) {
	if extchecktest.Is(os.Args[0], "opengrep") {
		os.Exit(extchecktest.Scan(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fake makes the fake opengrep the only one on PATH, printing the testdata
// file out for the project at dir.
func fake(t *testing.T, dir, out string, exit int, stderr string) string {
	t.Helper()
	return extchecktest.Fake(t, "opengrep", dir, out, exit, stderr)
}

const sqlRules = `rules:
  - id: python-sqli
    languages: [python]
    severity: ERROR
    message: SQL built from a string with user input. Use a parameterised query.
    pattern: $DB.execute("..." + $X + "...")
  - id: python-eval
    languages: [python]
    severity: WARNING
    message: eval of user input
    pattern: eval(...)
`

// project is a temp project with the rules file and the Python files that
// testdata/results.json, broken.json and badrule.json were recorded from:
// real runs of opengrep 1.33.1 on Windows (opengrep scan --json --quiet
// --config <file> <files>), with the project's folder made {{DIR}}.
// clean.json, noresults.json and notjson.txt are written by hand.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, ".opengrep/sql.yml", sqlRules)
	write(t, dir, "app/db.py", "import sqlite3\n\ndef find(db, name):\n    return db.execute(\"SELECT * FROM users WHERE name = '\" + name + \"'\")\n\neval(name)\n")
	write(t, dir, "app/label.py", "n = input()\nlabel = \"é\"; q = db.execute(\"SELECT \" + n + \"\")\n")
	write(t, dir, "app/broken.py", "def f(:\n  return db.execute(\"x\" + y + \"\")\n)))) @@@ $$$\n")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rule(id string, meta map[string]string) map[string]any {
	m := map[string]any{"check": "opengrep"}
	for k, v := range meta {
		m[k] = v
	}
	return map[string]any{"id": id, "body": "No SQL built from strings.", "scope": []string{"**/*.py"}, "meta": m}
}

// serve sends one request to the checker, as stele would, and decodes the
// answer as stele does.
func serve(t *testing.T, req map[string]any, v any) error {
	t.Helper()
	req["protocol"] = 1
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := extcheck.Serve(context.Background(), "v9.9.9", checker{}, bytes.NewReader(data), &out); code != 0 {
		t.Fatalf("Serve = %d", code)
	}
	return plugin.DecodeAnswer(out.Bytes(), v)
}

type problems struct {
	Problems []extcheck.Problem `json:"problems"`
}

type checked struct {
	Violations []extcheck.Violation `json:"violations"`
	Skipped    []extcheck.Skip      `json:"skipped"`
}

func TestDescribe(t *testing.T) {
	var info plugin.Info
	if err := serve(t, map[string]any{"op": "describe"}, &info); err != nil {
		t.Fatal(err)
	}
	if info.Kind != plugin.KindChecker || info.Version != "v9.9.9" || len(info.Protocols) != 1 || info.Protocols[0] != 1 {
		t.Errorf("describe = %+v", info)
	}
}

func TestValidate(t *testing.T) {
	dir := project(t)
	outside := t.TempDir()
	write(t, outside, "rules.yml", sqlRules)
	write(t, dir, "empty.yml", "rules: []\n")
	for _, c := range []struct {
		name string
		meta map[string]string
		want string // "" for no problem
	}{
		{"good", map[string]string{"rules": ".opengrep/sql.yml", "ids": "[python-sqli]"}, ""},
		{"no ids", map[string]string{"rules": ".opengrep/sql.yml"}, ""},
		{"one bare id", map[string]string{"rules": ".opengrep/sql.yml", "ids": "python-eval"}, ""},
		{"quoted", map[string]string{"rules": `".opengrep/sql.yml"`, "ids": `["python-sqli", 'python-eval']`}, ""},
		{"backslashes", map[string]string{"rules": `.opengrep\sql.yml`}, ""},
		{"blocking and message", map[string]string{"rules": ".opengrep/sql.yml", "blocking": "false", "message": "use parameters"}, ""},
		{"missing rules", map[string]string{"ids": "[python-sqli]"}, "rules: is missing"},
		{"absolute", map[string]string{"rules": filepath.Join(outside, "rules.yml")}, "is absolute"},
		{"slash", map[string]string{"rules": "/etc/rules.yml"}, "is absolute"},
		{"dotdot", map[string]string{"rules": "../rules.yml"}, `holds ".."`},
		{"inner dotdot", map[string]string{"rules": ".opengrep/../.opengrep/sql.yml"}, `holds ".."`},
		{"no such file", map[string]string{"rules": ".opengrep/nope.yml"}, "no such file"},
		{"a folder", map[string]string{"rules": ".opengrep"}, "is not a file"},
		{"no rule in it", map[string]string{"rules": "empty.yml"}, "holds no Opengrep rule"},
		{"unknown key", map[string]string{"rules": ".opengrep/sql.yml", "config": "p/python"}, "config: is not a key this checker knows"},
		{"unknown id", map[string]string{"rules": ".opengrep/sql.yml", "ids": "[python-sqli, python-xss]"}, `ids: "python-xss" is not the id of a rule in .opengrep/sql.yml`},
		{"empty ids", map[string]string{"rules": ".opengrep/sql.yml", "ids": "[]"}, "ids: is empty"},
		{"bad list", map[string]string{"rules": ".opengrep/sql.yml", "ids": "[python-sqli"}, "ids: unclosed list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var a problems
			if err := serve(t, map[string]any{"op": "validate", "project_dir": dir, "config": nil, "rules": []any{rule("r1", c.meta)}}, &a); err != nil {
				t.Fatal(err)
			}
			switch {
			case c.want == "" && len(a.Problems) > 0:
				t.Errorf("problems = %+v, want none", a.Problems)
			case c.want != "" && (len(a.Problems) != 1 || !strings.Contains(a.Problems[0].Message, c.want) || a.Problems[0].RuleID != "r1"):
				t.Errorf("problems = %+v, want one for r1 with %q", a.Problems, c.want)
			}
		})
	}
}

// A rules file that leads out of the project through a link is refused.
func TestValidateRefusesALinkOut(t *testing.T) {
	dir := project(t)
	outside := t.TempDir()
	write(t, outside, "rules.yml", sqlRules)
	if err := os.Symlink(filepath.Join(outside, "rules.yml"), filepath.Join(dir, "linked.yml")); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	var a problems
	if err := serve(t, map[string]any{"op": "validate", "project_dir": dir, "rules": []any{rule("r1", map[string]string{"rules": "linked.yml"})}}, &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Problems) != 1 || !strings.Contains(a.Problems[0].Message, "must stay inside the project") {
		t.Errorf("problems = %+v, want the link refused", a.Problems)
	}
}

func checkReq(dir string, rules ...any) map[string]any {
	return map[string]any{"op": "check", "project_dir": dir, "root": dir, "config": nil,
		"files": []string{"app/db.py", "app/label.py", "app/broken.py"}, "rules": rules}
}

func TestCheck(t *testing.T) {
	dir := project(t)
	log := fake(t, dir, "results.json", 0, "")
	var a checked
	err := serve(t, checkReq(dir,
		rule("sqli", map[string]string{"rules": ".opengrep/sql.yml", "ids": "[python-sqli]"}),
		rule("all", map[string]string{"rules": ".opengrep/sql.yml", "message": "fix it"}),
	), &a)
	if err != nil {
		t.Fatal(err)
	}
	want := []extcheck.Violation{
		{RuleID: "sqli", File: "app/db.py", Line: 4, Column: 12, Message: "python-sqli: SQL built from a string with user input. Use a parameterised query."},
		{RuleID: "all", File: "app/db.py", Line: 4, Column: 12, Message: "fix it"},
		// python-eval counts only for the rule with no ids.
		{RuleID: "all", File: "app/db.py", Line: 6, Column: 1, Message: "fix it"},
		// Opengrep's col, 19, counts bytes past the é; the column counts
		// characters.
		{RuleID: "sqli", File: "app/label.py", Line: 2, Column: 18, Message: "python-sqli: SQL built from a string with user input. Use a parameterised query."},
		{RuleID: "all", File: "app/label.py", Line: 2, Column: 18, Message: "fix it"},
	}
	if diff := cmp.Diff(want, a.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
	// A file Opengrep reports an error for is skipped, so stele says so.
	if len(a.Skipped) != 1 || a.Skipped[0].File != "app/broken.py" || a.Skipped[0].Reason != "opengrep: PartialParsing: Syntax error at line app/broken.py:3:" {
		t.Errorf("skipped = %+v, want app/broken.py", a.Skipped)
	}

	cs := extchecktest.Calls(t, log)
	if len(cs) != 1 {
		t.Fatalf("opengrep ran %d times, want once for one rules file", len(cs))
	}
	args := cs[0].Args
	wantArgs := []string{"scan", "--json", "--quiet", "--config", filepath.Join(dir, ".opengrep", "sql.yml"),
		filepath.Join(dir, "app", "db.py"), filepath.Join(dir, "app", "label.py"), filepath.Join(dir, "app", "broken.py")}
	if diff := cmp.Diff(wantArgs, args); diff != "" {
		t.Errorf("opengrep's arguments (-want +got):\n%s", diff)
	}
	for _, kv := range env {
		if !cs[0].HasEnv(kv) {
			t.Errorf("opengrep ran without %s", kv)
		}
	}
	if extcheck.Within(cs[0].Wd, dir) {
		t.Errorf("opengrep ran in the project: %s", cs[0].Wd)
	}
}

// Two rules files: Opengrep runs once for each.
func TestCheckRunsOncePerRulesFile(t *testing.T) {
	dir := project(t)
	write(t, dir, "other.yml", sqlRules)
	log := fake(t, dir, "clean.json", 0, "")
	var a checked
	err := serve(t, checkReq(dir,
		rule("a", map[string]string{"rules": ".opengrep/sql.yml"}),
		rule("b", map[string]string{"rules": "other.yml"}),
		rule("c", map[string]string{"rules": ".opengrep/sql.yml", "ids": "python-eval"}),
	), &a)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Violations) != 0 || len(a.Skipped) != 0 {
		t.Errorf("check = %+v, want nothing", a)
	}
	if cs := extchecktest.Calls(t, log); len(cs) != 2 {
		t.Errorf("opengrep ran %d times, want 2", len(cs))
	}
}

func TestCheckFailures(t *testing.T) {
	for _, c := range []struct {
		name, out string
		exit      int
		stderr    string
		want      string
	}{
		{"exit status", "badrule.json", 7, "[ERROR] invalid configuration\n", "opengrep exited with status 7: [ERROR] invalid configuration InvalidRuleSchemaError: Invalid rule schema: One of these properties is missing: 'message' SemgrepError: invalid configuration file found (1 configs were invalid)"},
		{"no output", "", 0, "", "opengrep wrote no JSON on stdout"},
		{"not JSON", "notjson.txt", 0, "", "opengrep's output is not the JSON expected"},
		{"no results", "noresults.json", 0, "", "opengrep's output has no results"},
		{"fatal error, exit 0", "badrule.json", 0, "", "opengrep: InvalidRuleSchemaError: Invalid rule schema"},
		{"crash", "", 139, "Segmentation fault\n", "opengrep exited with status 139: Segmentation fault"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := project(t)
			fake(t, dir, c.out, c.exit, c.stderr)
			var a checked
			err := serve(t, checkReq(dir, rule("r", map[string]string{"rules": ".opengrep/sql.yml"})), &a)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("check = %+v, %v; want an error with %q", a, err, c.want)
			}
		})
	}
}

// A finding in a file stele did not send is the scanner going wrong, not
// something to drop.
func TestCheckFindingInAnotherFile(t *testing.T) {
	dir := project(t)
	fake(t, dir, "results.json", 0, "")
	req := checkReq(dir, rule("r", map[string]string{"rules": ".opengrep/sql.yml"}))
	req["files"] = []string{"app/label.py"}
	var a checked
	if err := serve(t, req, &a); err == nil || !strings.Contains(err.Error(), "not one of the files sent") {
		t.Errorf("check = %+v, %v; want an error", a, err)
	}
}

func TestCheckWithoutOpengrep(t *testing.T) {
	dir := project(t)
	t.Setenv("PATH", t.TempDir())
	var a checked
	err := serve(t, checkReq(dir, rule("r", map[string]string{"rules": ".opengrep/sql.yml"})), &a)
	if err == nil || !strings.Contains(err.Error(), "opengrep: not found on PATH") || !strings.Contains(err.Error(), "install Opengrep") {
		t.Errorf("check = %+v, %v; want not found", a, err)
	}
}

// An opengrep inside the project is passed over, never run (D43).
func TestCheckPassesOverOpengrepInsideProject(t *testing.T) {
	dir := project(t)
	log := fake(t, dir, "clean.json", 0, "")
	extchecktest.Install(t, filepath.Join(dir, "bin"), "opengrep")
	var a checked
	err := serve(t, checkReq(dir, rule("r", map[string]string{"rules": ".opengrep/sql.yml"})), &a)
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("check = %+v, %v; want not found", a, err)
	}
	if cs := extchecktest.Calls(t, log); len(cs) != 0 {
		t.Errorf("the opengrep inside the project ran: %+v", cs)
	}
}

// A rule check can't use fails the call, though stele validates first.
func TestCheckRefusesABadRule(t *testing.T) {
	dir := project(t)
	fake(t, dir, "clean.json", 0, "")
	var a checked
	err := serve(t, checkReq(dir, rule("r", map[string]string{"rules": "../x.yml"})), &a)
	if err == nil || !strings.Contains(err.Error(), "rule r: rules:") {
		t.Errorf("check = %+v, %v; want an error", a, err)
	}
}

func TestMatchID(t *testing.T) {
	ids := []string{"python-sqli", "sqli", "a.b"}
	for checkID, want := range map[string]string{
		"python-sqli":                     "python-sqli",
		"opengrep.python-sqli":            "python-sqli",
		"tmp.x.sqli":                      "sqli",
		"c.d.a.b":                         "a.b",
		"python-sqli-2":                   "",
		"opengrep.python-sqlix":           "",
		"Users.me.project.opengrep.other": "",
	} {
		if got := matchID(checkID, ids); got != want {
			t.Errorf("matchID(%q) = %q, want %q", checkID, got, want)
		}
	}
}

func TestFileIDs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "r.yml", sqlRules+"  -\n    id: \"quoted-id\"\n")
	write(t, dir, "r.json", `{"rules": [{"id": "json-one", "pattern": "x"}, {"pattern": "y", "id": "json-two"}]}`)
	for f, want := range map[string][]string{
		"r.yml":  {"python-sqli", "python-eval", "quoted-id"},
		"r.json": {"json-one", "json-two"},
	} {
		got, err := fileIDs(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", f, diff)
		}
	}
}

func TestChunks(t *testing.T) {
	dir := t.TempDir()
	var rel []string
	for i := 0; i < 2000; i++ {
		rel = append(rel, fmt.Sprintf("some/rather/long/folder/name/file%04d.py", i))
	}
	fs := extcheck.NewFiles(dir, rel)
	got := chunks(rel, fs)
	n := 0
	for _, c := range got {
		size := 0
		for _, p := range c {
			size += len(p) + 1
		}
		if size > maxArgs() {
			t.Errorf("a chunk of %d bytes, over %d", size, maxArgs())
		}
		n += len(c)
	}
	if n != len(rel) || len(got) < 2 {
		t.Errorf("%d chunks holding %d files, want several holding %d", len(got), n, len(rel))
	}
}
