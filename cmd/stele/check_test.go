package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/lock"
)

// checkRules has a blocking regex rule for Go files, and a non-blocking one
// for every file. stele.md itself holds both patterns' text, and is never
// checked against them.
const checkRules = `check: regex
pattern: password\s*=
scope: ["**/*.go"]

Never put credentials in code: password = ...

---

check: regex
pattern: TODO
blocking: false
message: track it in an issue

No TODO comments.

---

Guidance with no check.
`

// gitProject makes a real git repository holding stele.md, compiled and
// committed, and enters it.
func gitProject(t *testing.T, steleMD string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := project(t, steleMD)
	if err := os.Remove(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "init", "-q")
	gitT(t, root, "config", "user.email", "t@example.com")
	gitT(t, root, "config", "user.name", "t")
	gitT(t, root, "config", "commit.gpgsign", "false")
	gitT(t, root, "config", "core.autocrlf", "false")
	if steleMD != "" {
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile = %d, stderr %q", code, errOut)
		}
	}
	commitAll(t, root, "rules")
	return root
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func commitAll(t *testing.T, root, msg string) {
	t.Helper()
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-q", "--allow-empty", "-m", msg)
}

func writeAt(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ruleID is the id stele.lock gave the rule at position.
func ruleID(t *testing.T, root string, position int) string {
	t.Helper()
	lf, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range lf.Rules {
		if e.Position == position {
			return e.ID
		}
	}
	t.Fatalf("no rule at position %d", position)
	return ""
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		steleMD  string
		compile  bool   // run stele compile first
		after    string // stele.md content written after compiling
		lock     string // stele.lock content written instead of compiling
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "valid", steleMD: rules, compile: true, wantCode: exitOK, wantOut: "2 rules valid"},
		{name: "never compiled", steleMD: rules, wantCode: exitFailed, wantErr: "stele.lock is out of date with the rules: run stele compile"},
		{name: "rules changed since compile", steleMD: rules, compile: true, after: rules + "\n---\n\nA third rule.\n", wantCode: exitFailed, wantErr: "out of date"},
		{name: "parse error", steleMD: "tags: [a]\n", wantCode: exitFailed, wantErr: "stele.md:1: rule has no body"},
		{
			name:    "duplicate id",
			steleMD: rules,
			lock: `{"version": 1, "project": "p", "rules": [
				{"id": "a", "source": "stelemd", "hash": "h1", "position": 0},
				{"id": "a", "source": "stelemd", "hash": "h2", "position": 1}]}`,
			wantCode: exitFailed,
			wantErr:  "duplicate rule id a",
		},
		{name: "invalid lock", steleMD: rules, lock: "{", wantCode: exitFailed, wantErr: "stele.lock: invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gitProject(t, "")
			writeAt(t, root, "stele.md", tt.steleMD)
			if tt.compile {
				if code, _, errOut := runCmd("compile"); code != exitOK {
					t.Fatalf("compile = %d, stderr %q", code, errOut)
				}
			}
			if tt.after != "" {
				writeAt(t, root, "stele.md", tt.after)
			}
			if tt.lock != "" {
				writeAt(t, root, "stele.lock", tt.lock)
			}
			before := snapshot(t, root)

			code, out, errOut := runCmd("check")
			if code != tt.wantCode {
				t.Errorf("check = %d, want %d (stderr %q)", code, tt.wantCode, errOut)
			}
			if !contains(out, tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out, tt.wantOut)
			}
			if tt.wantErr != "" && !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut, tt.wantErr)
			}
			if after := snapshot(t, root); after != before {
				t.Errorf("check wrote files:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestCheckChangedFiles(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "a.go", "package a\n")
	writeAt(t, root, "old.go", "password = 1\n")
	writeAt(t, root, "gone.go", "package gone\n")
	writeAt(t, root, ".gitignore", "ignored.go\n")
	commitAll(t, root, "code")

	writeAt(t, root, "a.go", "package a\n\npassword = 2\n") // modified
	writeAt(t, root, "new.go", "x := 1; password = 3\n")    // untracked
	writeAt(t, root, "staged.go", "password = 4\n")         // staged
	writeAt(t, root, "ignored.go", "password = 5\n")        // gitignored
	writeAt(t, root, "notes.txt", "TODO later\n")           // non-blocking
	gitT(t, root, "add", "staged.go")
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)

	code, out, errOut := runCmd("check")
	if code != exitFailed {
		t.Fatalf("check = %d, want 1 (stderr %q, stdout %q)", code, errOut, out)
	}
	id := ruleID(t, root, 0)
	for _, want := range []string{
		`a.go:3:1: error ` + id + ` "Never put credentials in code: password = ..."` + "\n",
		`new.go:1:9: error `,
		`staged.go:1:1: error `,
		`notes.txt:1:1: warning ` + ruleID(t, root, 1) + ` "No TODO comments.": track it in an issue`,
		"3 rules valid",
		"checked 4 files: 3 errors, 1 warning",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	for _, unwanted := range []string{"old.go", "ignored.go", "gone.go", "stele.md"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("stdout = %q, want no %s", out, unwanted)
		}
	}
	if after := snapshot(t, root); after != before {
		t.Error("check wrote files")
	}

	// Only a warning: exit 0.
	gitT(t, root, "checkout", "-q", "--", "a.go")
	for _, f := range []string{"new.go", "ignored.go"} {
		os.Remove(filepath.Join(root, f))
	}
	gitT(t, root, "rm", "-q", "--cached", "staged.go")
	os.Remove(filepath.Join(root, "staged.go"))
	code, out, _ = runCmd("check")
	if code != exitOK || !strings.Contains(out, "warning") {
		t.Errorf("warnings only: check = %d, stdout %q, want 0 and the warning", code, out)
	}
}

func TestCheckNoCommits(t *testing.T) {
	root := gitProject(t, checkRules)
	gitT(t, root, "update-ref", "-d", "HEAD")
	writeAt(t, root, "a.go", "password = 1\n")
	code, out, errOut := runCmd("check")
	if code != exitFailed || !strings.Contains(out, "a.go:1:1: error") {
		t.Errorf("no commits: check = %d, stdout %q, stderr %q, want every file checked", code, out, errOut)
	}
}

func TestCheckBase(t *testing.T) {
	root := gitProject(t, checkRules)
	gitT(t, root, "branch", "-M", "main")
	gitT(t, root, "checkout", "-q", "-b", "feature")
	writeAt(t, root, "mine.go", "password = 1\n")
	commitAll(t, root, "feature work")
	gitT(t, root, "checkout", "-q", "main")
	writeAt(t, root, "theirs.go", "password = 2\n")
	commitAll(t, root, "main moved on")
	gitT(t, root, "checkout", "-q", "feature")

	code, out, errOut := runCmd("check", "--base", "main")
	if code != exitFailed || !strings.Contains(out, "mine.go:1:1: error") || strings.Contains(out, "theirs.go") {
		t.Errorf("--base main = %d, stdout %q, stderr %q, want mine.go only", code, out, errOut)
	}

	code, _, errOut = runCmd("check", "--base", "nope")
	if code != exitCantRun || !strings.Contains(errOut, "unknown ref") {
		t.Errorf("--base nope = %d, stderr %q, want 2 and unknown ref", code, errOut)
	}
	code, _, errOut = runCmd("check", "--base", "--output=x")
	if code != exitCantRun || !strings.Contains(errOut, "is not a ref") {
		t.Errorf("--base --output=x = %d, stderr %q, want 2", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Error("git was handed --output=x")
	}
}

func TestCheckAll(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "old.go", "password = 1\n")
	commitAll(t, root, "code")
	writeAt(t, root, "untracked.go", "password = 2\n")

	code, out, _ := runCmd("check", "--all")
	if code != exitFailed || !strings.Contains(out, "old.go:1:1: error") || strings.Contains(out, "untracked.go") {
		t.Errorf("--all = %d, stdout %q, want old.go and not untracked.go", code, out)
	}
}

func TestCheckPaths(t *testing.T) {
	root := project(t, checkRules) // no git: paths need none
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	writeAt(t, root, "src/a.go", "password = 1\n")
	writeAt(t, root, "src/b.go", "password = 2\n")
	t.Chdir(filepath.Join(root, "src"))

	code, out, errOut := runCmd("check", "a.go")
	if code != exitFailed || !strings.Contains(out, "src/a.go:1:1: error") || strings.Contains(out, "b.go") {
		t.Errorf("check a.go = %d, stdout %q, stderr %q, want src/a.go only", code, out, errOut)
	}

	for _, tt := range []struct{ arg, want string }{
		{filepath.Join(t.TempDir(), "x.go"), "outside the project"},
		{"missing.go", "no such file"},
		{".", "is a directory"},
	} {
		code, _, errOut := runCmd("check", tt.arg)
		if code != exitCantRun || !strings.Contains(errOut, tt.want) {
			t.Errorf("check %s = %d, stderr %q, want 2 and %q", tt.arg, code, errOut, tt.want)
		}
	}
}

func TestCheckFlags(t *testing.T) {
	project(t, checkRules)
	for _, args := range [][]string{
		{"--all", "--base", "main"},
		{"--all", "a.go"},
		{"--base", "main", "a.go"},
		{"--format", "xml"},
		{"--push"},
	} {
		if code, _, errOut := runCmd(append([]string{"check"}, args...)...); code != exitCantRun {
			t.Errorf("check %v = %d, stderr %q, want 2", args, code, errOut)
		}
	}
}

func TestCheckNoGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := project(t, checkRules)
	if err := os.Remove(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCmd("compile"); code != exitOK {
		t.Fatal("compile failed")
	}
	code, _, errOut := runCmd("check")
	if code != exitCantRun || !strings.Contains(errOut, "not a git repository") {
		t.Errorf("check outside git = %d, stderr %q, want 2", code, errOut)
	}
}

func TestCheckJSON(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "a.go", "password = 1\n")

	code, out, errOut := runCmd("check", "--format", "json")
	if code != exitFailed {
		t.Fatalf("check = %d, stderr %q", code, errOut)
	}
	var rep check.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	lf, _ := lock.Load(root)
	if rep.V != 1 || rep.Project != lf.Project || rep.Project == "" {
		t.Errorf("v %d project %q, want 1 and %q", rep.V, rep.Project, lf.Project)
	}
	want := check.Finding{RuleID: ruleID(t, root, 0), Excerpt: "Never put credentials in code: password = ...", File: "a.go",
		Line: 1, Column: 1, Message: "Never put credentials in code: password = ...", Blocking: true}
	if len(rep.Violations) != 1 || rep.Violations[0] != want {
		t.Errorf("violations = %+v, want %+v", rep.Violations, want)
	}

	// A rule file stele cannot use is a problem with no rule id.
	writeAt(t, root, "stele.md", checkRules+"\n---\n\nA new rule.\n")
	code, out, _ = runCmd("check", "--format", "json")
	rep = check.Report{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if code != exitFailed || len(rep.Problems) != 1 || rep.Problems[0].RuleID != "" || !strings.Contains(rep.Problems[0].Message, "out of date") || rep.Project != lf.Project {
		t.Errorf("stale lock: check = %d, report %+v", code, rep)
	}
}

func TestCheckInvalidRules(t *testing.T) {
	tests := []struct{ name, meta, want string }{
		{"unknown checker", "check: semgrep", `check: "semgrep" is not a checker; the only one is the built-in regex`},
		{"broken pattern", "check: regex\npattern: (", "pattern does not compile"},
		{"no pattern", "check: regex", "no pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gitProject(t, tt.meta+"\n\nSome rule.\n")
			writeAt(t, root, "a.go", "x\n")
			code, out, errOut := runCmd("check")
			if code != exitFailed || !strings.Contains(out, tt.want) || !strings.Contains(out, `"Some rule." (stelemd rule 1)`) {
				t.Errorf("check = %d, stdout %q, stderr %q, want 1 and %q", code, out, errOut, tt.want)
			}
		})
	}
}

func TestCheckSubdirRoot(t *testing.T) {
	root := gitProject(t, "")
	writeAt(t, root, "sub/stele.md", checkRules)
	writeAt(t, root, "outer.go", "password = 1\n")
	writeAt(t, root, "sub/inner.go", "password = 2\n")
	t.Chdir(filepath.Join(root, "sub"))
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	code, out, errOut := runCmd("check")
	if code != exitFailed || !strings.Contains(out, "\ninner.go:1:1: error") && !strings.HasPrefix(out, "inner.go:1:1: error") || strings.Contains(out, "outer.go") {
		t.Errorf("check in sub = %d, stdout %q, stderr %q, want inner.go only, relative to sub", code, out, errOut)
	}
}

func TestCheckSkipsNestedProjects(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "inner/stele.md", "Guidance only.\n")
	writeAt(t, root, "inner/deep/a.go", "password = 1\n")
	writeAt(t, root, "b.go", "password = 2\n")
	code, out, _ := runCmd("check")
	if code != exitFailed || !strings.Contains(out, "b.go") || strings.Contains(out, "inner/") {
		t.Errorf("check = %d, stdout %q, want b.go and nothing from the nested project", code, out)
	}
}

func TestCheckSkipsBinary(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "blob.go", "\x00password = 1\n")
	code, out, _ := runCmd("check")
	if code != exitOK || strings.Contains(out, "blob.go") {
		t.Errorf("check = %d, stdout %q, want the binary file skipped", code, out)
	}
}
