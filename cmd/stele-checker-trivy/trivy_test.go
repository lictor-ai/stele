package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lictor-ai/stele/internal/extcheck"
	"github.com/lictor-ai/stele/internal/extcheck/extchecktest"
	"github.com/lictor-ai/stele/internal/plugin"
)

// TestMain makes this test binary a fake trivy when it is started under
// that name (extchecktest). The real Trivy never runs here.
func TestMain(m *testing.M) {
	if extchecktest.Is(os.Args[0], "trivy") {
		os.Exit(extchecktest.Scan(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func fake(t *testing.T, dir, out string, exit int, stderr string) string {
	t.Helper()
	return extchecktest.Fake(t, "trivy", dir, out, exit, stderr)
}

// project is a temp project like the one testdata/report.json was recorded
// from: a real run of trivy 0.76.2 on Windows (trivy fs --format json
// --quiet --exit-code 0 --offline-scan --scanners vuln,secret,misconfig),
// with the project's folder made {{DIR}}. The token Trivy found was a made
// up one, and Trivy's report hides it. clean.json, noschema.json and
// notjson.txt are written by hand.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "requirements.txt", "flask==0.12\nrequests==2.31.0\n")
	write(t, dir, "Dockerfile", "FROM alpine:latest\nRUN apk add curl\n")
	write(t, dir, "config/settings.py", "import os\n\nGITHUB_TOKEN = \"not-a-real-token\"\n")
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
	m := map[string]any{"check": "trivy"}
	for k, v := range meta {
		m[k] = v
	}
	return map[string]any{"id": id, "body": "No known vulnerable dependency.", "scope": []string{"**"}, "meta": m}
}

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
	if info.Kind != plugin.KindChecker || info.Version != "v9.9.9" {
		t.Errorf("describe = %+v", info)
	}
}

func TestValidate(t *testing.T) {
	dir := project(t)
	for _, c := range []struct {
		name string
		meta map[string]string
		want string
	}{
		{"all three", map[string]string{"scanners": "[vuln, secret, misconfig]"}, ""},
		{"one bare", map[string]string{"scanners": "secret"}, ""},
		{"severity", map[string]string{"scanners": "[vuln]", "severity": "[HIGH, critical]"}, ""},
		{"message", map[string]string{"scanners": "[vuln]", "message": "upgrade it", "blocking": "false"}, ""},
		{"no scanners", map[string]string{"severity": "[HIGH]"}, "scanners: is missing"},
		{"empty scanners", map[string]string{"scanners": "[]"}, "scanners: is empty"},
		{"unknown scanner", map[string]string{"scanners": "[vuln, license]"}, `scanners: "license" is not one of vuln, secret, misconfig`},
		{"bad severity", map[string]string{"scanners": "vuln", "severity": "[SEVERE]"}, `severity: "SEVERE" is not one of UNKNOWN, LOW, MEDIUM, HIGH, CRITICAL`},
		{"empty severity", map[string]string{"scanners": "vuln", "severity": "[]"}, "severity: is empty"},
		{"unknown key", map[string]string{"scanners": "vuln", "skip-dirs": "node_modules"}, "skip-dirs: is not a key this checker knows"},
		{"bad list", map[string]string{"scanners": "[vuln"}, "scanners: unclosed list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var a problems
			if err := serve(t, map[string]any{"op": "validate", "project_dir": dir, "rules": []any{rule("r1", c.meta)}}, &a); err != nil {
				t.Fatal(err)
			}
			switch {
			case c.want == "" && len(a.Problems) > 0:
				t.Errorf("problems = %+v, want none", a.Problems)
			case c.want != "" && (len(a.Problems) != 1 || !strings.Contains(a.Problems[0].Message, c.want)):
				t.Errorf("problems = %+v, want one with %q", a.Problems, c.want)
			}
		})
	}
}

func checkReq(dir string, files []string, rules ...any) map[string]any {
	return map[string]any{"op": "check", "project_dir": dir, "root": dir, "files": files, "rules": rules}
}

var allFiles = []string{"requirements.txt", "Dockerfile", "config/settings.py"}

func TestCheck(t *testing.T) {
	dir := project(t)
	log := fake(t, dir, "report.json", 0, "")
	var a checked
	err := serve(t, checkReq(dir, allFiles,
		rule("deps", map[string]string{"scanners": "[vuln]", "severity": "[critical, HIGH]"}),
		rule("code", map[string]string{"scanners": "[secret, misconfig]", "message": ""}),
	), &a)
	if err != nil {
		t.Fatal(err)
	}
	// The fake prints the whole report for both runs: each rule takes only
	// its own scanners and severities.
	want := []extcheck.Violation{
		// A vulnerable package has no line in Trivy's report: it is placed
		// on the line of the file that names it.
		{RuleID: "deps", File: "requirements.txt", Line: 1, Column: 1, Message: "CVE-2018-1000656 (HIGH) in flask 0.12, fixed in 0.12.3: python-flask: Denial of Service via crafted JSON file"},
		{RuleID: "deps", File: "requirements.txt", Line: 1, Column: 1, Message: "CVE-2019-1010083 (HIGH) in flask 0.12, fixed in 1.0: python-flask: unexpected memory usage can lead to denial of service via crafted encoded JSON data"},
		{RuleID: "deps", File: "requirements.txt", Line: 1, Column: 1, Message: "CVE-2023-30861 (HIGH) in flask 0.12, fixed in 2.3.2, 2.2.5: flask: Possible disclosure of permanent session cookie due to missing Vary: Cookie header"},
		// A misconfiguration with no line is on line 1.
		{RuleID: "code", File: "Dockerfile", Line: 1, Column: 1, Message: "DS-0001 (MEDIUM): ':latest' tag used: Specify a tag in the 'FROM' statement for image 'alpine'"},
		{RuleID: "code", File: "Dockerfile", Line: 1, Column: 1, Message: "DS-0002 (HIGH): Image user should not be 'root': Specify at least 1 USER command in Dockerfile with non-root user as argument"},
		{RuleID: "code", File: "Dockerfile", Line: 2, Column: 1, Message: "DS-0025 (HIGH): 'apk add' is missing '--no-cache': '--no-cache' is missed: apk add curl"},
		{RuleID: "code", File: "Dockerfile", Line: 1, Column: 1, Message: "DS-0026 (LOW): No HEALTHCHECK defined: Add HEALTHCHECK instruction in your Dockerfile"},
		{RuleID: "code", File: "config/settings.py", Line: 3, Column: 1, Message: "secret github-pat (CRITICAL): GitHub Personal Access Token"},
	}
	if diff := cmp.Diff(want, a.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}

	cs := extchecktest.Calls(t, log)
	if len(cs) != 2 {
		t.Fatalf("trivy ran %d times, want once per set of scanners", len(cs))
	}
	wantArgs := [][]string{
		{"fs", "--format", "json", "--quiet", "--exit-code", "0", "--offline-scan", "--scanners", "vuln", "--severity", "HIGH,CRITICAL", dir},
		{"fs", "--format", "json", "--quiet", "--exit-code", "0", "--offline-scan", "--scanners", "misconfig,secret", dir},
	}
	for i, c := range cs {
		if diff := cmp.Diff(wantArgs[i], c.Args); diff != "" {
			t.Errorf("run %d's arguments (-want +got):\n%s", i, diff)
		}
		for _, kv := range env {
			if !c.HasEnv(kv) {
				t.Errorf("trivy ran without %s", kv)
			}
		}
		if extcheck.Within(c.Wd, dir) {
			t.Errorf("trivy ran in the project: %s", c.Wd)
		}
	}
}

// Trivy scans the whole project; only the files stele sent are reported,
// and a rule's own message replaces Trivy's.
func TestCheckKeepsToTheFilesSent(t *testing.T) {
	dir := project(t)
	fake(t, dir, "report.json", 0, "")
	var a checked
	err := serve(t, checkReq(dir, []string{"config/settings.py"},
		rule("all", map[string]string{"scanners": "[vuln, secret, misconfig]", "message": "no secrets in code"})), &a)
	if err != nil {
		t.Fatal(err)
	}
	want := []extcheck.Violation{{RuleID: "all", File: "config/settings.py", Line: 3, Column: 1, Message: "no secrets in code"}}
	if diff := cmp.Diff(want, a.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
}

func TestCheckClean(t *testing.T) {
	dir := project(t)
	fake(t, dir, "clean.json", 0, "")
	var a checked
	if err := serve(t, checkReq(dir, allFiles, rule("r", map[string]string{"scanners": "vuln"})), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Violations) != 0 {
		t.Errorf("violations = %+v, want none", a.Violations)
	}
}

func TestPositionOfAPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package-lock.json", "{\n  \"packages\": {\n    \"node_modules/lodash\": {\n      \"version\": \"4.17.20\",\n      \"resolved\": \"https://registry.npmjs.org/lodash/-/lodash-4.17.20.tgz\"\n    }\n  }\n}\n")
	write(t, dir, "go.mod", "module x\n\nrequire (\n\tgolang.org/x/net v0.1.0\n)\n")
	p := positions{}
	for _, c := range []struct {
		file, pkg, version string
		line, col          int
	}{
		{"package-lock.json", "lodash", "4.17.20", 5, 47},   // the line with both
		{"package-lock.json", "lodash", "9.9.9", 3, 19},     // else the first naming it
		{"package-lock.json", "left-pad", "1.0.0", 1, 1},    // else line 1
		{"go.mod", "golang.org/x/net", "v0.1.0", 4, 2},      // a tab is one character
		{"missing.txt", "golang.org/x/net", "v0.1.0", 1, 1}, // a file it can't read
	} {
		line, col := p.of(filepath.Join(dir, c.file), c.pkg, c.version)
		if line != c.line || col != c.col {
			t.Errorf("%s %s: %d:%d, want %d:%d", c.file, c.pkg, line, col, c.line, c.col)
		}
	}
}

func TestCheckFailures(t *testing.T) {
	for _, c := range []struct {
		name, out string
		exit      int
		stderr    string
		want      string
	}{
		{"exit status", "", 1, "2026-11-09T10:00:00Z\tFATAL\tFatal error\trun error: init error: DB error: failed to download vulnerability DB\n", "trivy exited with status 1: 2026-11-09T10:00:00Z FATAL Fatal error run error: init error: DB error: failed to download vulnerability DB"},
		{"no output", "", 0, "", "trivy wrote no JSON on stdout"},
		{"not JSON", "notjson.txt", 0, "", "trivy's output is not the JSON expected"},
		{"not a report", "noschema.json", 0, "", "trivy's output is not a JSON report: no SchemaVersion"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := project(t)
			fake(t, dir, c.out, c.exit, c.stderr)
			var a checked
			err := serve(t, checkReq(dir, allFiles, rule("r", map[string]string{"scanners": "vuln"})), &a)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("check = %+v, %v; want an error with %q", a, err, c.want)
			}
		})
	}
}

func TestCheckWithoutTrivy(t *testing.T) {
	dir := project(t)
	t.Setenv("PATH", t.TempDir())
	var a checked
	err := serve(t, checkReq(dir, allFiles, rule("r", map[string]string{"scanners": "vuln"})), &a)
	if err == nil || !strings.Contains(err.Error(), "trivy: not found on PATH") || !strings.Contains(err.Error(), "install Trivy") {
		t.Errorf("check = %+v, %v; want not found", a, err)
	}
}

// A trivy inside the project is passed over, never run (D43).
func TestCheckPassesOverTrivyInsideProject(t *testing.T) {
	dir := project(t)
	log := fake(t, dir, "clean.json", 0, "")
	extchecktest.Install(t, filepath.Join(dir, "tools"), "trivy")
	var a checked
	err := serve(t, checkReq(dir, allFiles, rule("r", map[string]string{"scanners": "vuln"})), &a)
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("check = %+v, %v; want not found", a, err)
	}
	if cs := extchecktest.Calls(t, log); len(cs) != 0 {
		t.Errorf("the trivy inside the project ran: %+v", cs)
	}
}
