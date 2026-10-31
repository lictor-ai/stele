package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
		// An invalid rule exits 1, but the files are written all the same.
		if code, _, errOut := runCmd("compile"); code != exitOK && code != exitFailed {
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
		name      string
		steleMD   string
		noMD      bool   // write no stele.md at all
		steleJSON string // stele.json content, if any
		compile   bool   // run stele compile first
		after     string // stele.md content written after compiling
		removeMD  bool   // remove stele.md after compiling
		lock      string // stele.lock content written instead of compiling
		wantCode  int
		wantOut   string
		wantErr   string
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
		// A stele project without its lock still needs one: these are not
		// "nothing to check" (#49).
		{name: "empty stele.md", steleMD: "", wantCode: exitFailed, wantErr: "out of date"},
		{name: "stele.json only", noMD: true, steleJSON: "{}", wantCode: exitFailed, wantErr: "out of date"},
		{name: "stele.lock only", steleMD: rules, compile: true, removeMD: true, wantCode: exitFailed, wantErr: "out of date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gitProject(t, "")
			if !tt.noMD {
				writeAt(t, root, "stele.md", tt.steleMD)
			}
			if tt.steleJSON != "" {
				writeAt(t, root, "stele.json", tt.steleJSON)
			}
			if tt.compile {
				if code, _, errOut := runCmd("compile"); code != exitOK {
					t.Fatalf("compile = %d, stderr %q", code, errOut)
				}
			}
			if tt.after != "" {
				writeAt(t, root, "stele.md", tt.after)
			}
			if tt.removeMD {
				if err := os.Remove(filepath.Join(root, "stele.md")); err != nil {
					t.Fatal(err)
				}
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

// A pattern keeps its quotes (#40): "net/http" matches the import, not every
// mention of net/http.
func TestCheckQuotedPattern(t *testing.T) {
	root := project(t, "check: regex\npattern: \"net/http\"\n\nDo not import net/http.\n")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	writeAt(t, root, "a.go", "package a\n\nimport \"net/http\"\n")
	writeAt(t, root, "b.go", "package a\n\n// see the net/http docs\n")

	code, out, errOut := runCmd("check", "a.go", "b.go")
	if code != exitFailed || !strings.Contains(out, "a.go:3:8: error") || strings.Contains(out, "b.go") {
		t.Errorf("check = %d, stdout %q, stderr %q, want a.go:3:8 only", code, out, errOut)
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

// plantGit puts a program named git in dir, which writes a marker if it runs,
// and returns the marker's path.
func plantGit(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n: > \"$0.ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p + ".ran"
}

// A cloned repository can bring a git; a PATH entry for its bin directory,
// which direnv can add, would run it.
func TestCheckRefusesGitInsideProject(t *testing.T) {
	root := gitProject(t, checkRules)
	bin := filepath.Join(root, "bin")
	ran := plantGit(t, bin)
	writeAt(t, root, "a.go", "package a\n")
	t.Setenv("PATH", bin)

	for _, args := range [][]string{{"check"}, {"check", "--all"}, {"check", "--base", "HEAD"}} {
		code, _, errOut := runCmd(args...)
		if code != exitCantRun || !strings.Contains(errOut, "inside the project") {
			t.Errorf("%v = %d, stderr %q, want 2 and inside the project", args, code, errOut)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the planted git ran")
	}
	// Paths need no git.
	if code, _, errOut := runCmd("check", "a.go"); code != exitOK {
		t.Errorf("check a.go = %d, stderr %q", code, errOut)
	}
}

func TestCheckRefusesGitInEnclosingRepository(t *testing.T) {
	root := gitProject(t, checkRules)
	sub := filepath.Join(root, "svc")
	writeAt(t, root, "svc/stele.md", checkRules)
	t.Chdir(sub)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	bin := filepath.Join(root, "bin")
	plantGit(t, bin)
	t.Setenv("PATH", bin)
	code, _, errOut := runCmd("check")
	if code != exitCantRun || !strings.Contains(errOut, "inside the project") {
		t.Errorf("check = %d, stderr %q, want 2 and inside the project", code, errOut)
	}
}

// A clone can hold a folder laid out as a repository, whose config names a
// program: git never checks out a .git, but it does check out HEAD, config,
// objects/ and refs/. stele check run there must not start the program.
func TestCheckRunsNoProgramFromRepositoryConfig(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "sub/stele.md", checkRules)
	writeAt(t, root, "sub/HEAD", "ref: refs/heads/main\n")
	writeAt(t, root, "sub/objects/keep", "")
	writeAt(t, root, "sub/refs/keep", "")
	// What core.fsmonitor names goes through a shell, on Windows too: the
	// marker needs no program built for this test. The # drops git's arguments.
	writeAt(t, root, "sub/config", "[core]\n\trepositoryformatversion = 0\n\tbare = false\n\tworktree = .\n\tfsmonitor = echo ran > fsmonitor-ran #\n")
	t.Chdir(filepath.Join(root, "sub"))
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	t.Chdir(root)
	commitAll(t, root, "a folder laid out as a repository")

	clone := filepath.Join(t.TempDir(), "clone")
	gitT(t, root, "clone", "-q", root, clone)
	sub := filepath.Join(clone, "sub")
	t.Chdir(sub)
	code, out, errOut := runCmd("check")
	if _, err := os.Stat(filepath.Join(sub, "fsmonitor-ran")); err == nil {
		t.Errorf("the program in the repository's config ran: check = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if code != exitCantRun || !strings.Contains(errOut, "bare repository") {
		t.Errorf("check = %d, stdout %q, stderr %q, want 2 and bare repository", code, out, errOut)
	}
}

// A partial clone fetches the blobs a diff needs from its promisor, here to
// find a rename. That fetch is the user's own, over whatever transport they
// cloned with: stele check must not forbid it, as stele publish forbids
// everything but https and ssh.
func TestCheckPartialCloneFetchesFromItsPromisor(t *testing.T) {
	root := gitProject(t, checkRules)
	gitT(t, root, "config", "uploadpack.allowFilter", "true")
	body := strings.Repeat("a line that stays through the rename\n", 40)
	writeAt(t, root, "old.txt", body)
	commitAll(t, root, "base")
	gitT(t, root, "branch", "base")
	if err := os.Rename(filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
	writeAt(t, root, "new.txt", body+"one more\n")
	commitAll(t, root, "rename and edit")

	clone := filepath.Join(t.TempDir(), "clone")
	gitT(t, root, "clone", "-q", "--filter=blob:none", "file:///"+strings.TrimPrefix(filepath.ToSlash(root), "/"), clone)
	t.Chdir(clone)
	if code, out, errOut := runCmd("check", "--base", "origin/base"); code != exitOK {
		t.Errorf("check = %d, stdout %q, stderr %q, want 0", code, out, errOut)
	}
}

// With ~/.git, dotfiles for example, git in ~/bin is the user's own: a
// project at ~/notes is not inside that repository.
func TestGitBinKeepsGitOutsideAHomeRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	notes := filepath.Join(home, "notes")
	for _, dir := range []string{filepath.Join(home, ".git"), notes} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(home, "bin")
	plantGit(t, bin)
	t.Setenv("PATH", bin)
	if _, err := gitBin(notes); err != nil {
		t.Errorf("gitBin = %v, want the git in ~/bin", err)
	}
}

func TestCheckNoGitOnPath(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "a.go", "package a\n")
	t.Setenv("PATH", t.TempDir())
	code, _, errOut := runCmd("check")
	if code != exitCantRun || !strings.Contains(errOut, "pass paths instead") {
		t.Errorf("check = %d, stderr %q, want 2 and the hint", code, errOut)
	}
}

// git finds what it starts on PATH, as stele finds git.
func TestGitToolDropsPathInsideProject(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	plantGit(t, outside)
	t.Setenv("PATH", outside+string(os.PathListSeparator)+filepath.Join(root, "bin"))
	g, err := newGitTool(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := g.command("version")
	if !strings.EqualFold(filepath.Dir(cmd.Path), outside) || cmd.Dir != root {
		t.Errorf("cmd = %s in %s", cmd.Path, cmd.Dir)
	}
	var got []string
	for _, kv := range cmd.Env {
		if name, value, _ := strings.Cut(kv, "="); strings.EqualFold(name, "PATH") {
			got = append(got, value)
		}
	}
	if len(got) != 1 || got[0] != outside {
		t.Errorf("PATH = %q, want %q", got, outside)
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
	writeAt(t, root, "inner/deep/c.go", "x\n")
	// A directory named stele.json is not a subproject: it cannot hide files.
	if err := os.MkdirAll(filepath.Join(root, "fake", "stele.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(t, root, "fake/d.go", "password = 3\n")

	code, out, _ := runCmd("check")
	if code != exitFailed || !strings.Contains(out, "b.go:1:1: error") || strings.Contains(out, "inner/deep") {
		t.Errorf("check = %d, stdout %q, want b.go and no file from the nested project", code, out)
	}
	for _, want := range []string{
		"inner/: skipped 3 files, it has its own stele.md\n",
		"fake/d.go:1:1: error",
		"checked 2 files: 2 errors",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}

	_, out, _ = runCmd("check", "--format", "json")
	var rep check.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	want := []check.Skip{
		{File: "inner/deep/a.go", Reason: "in subproject inner/"},
		{File: "inner/deep/c.go", Reason: "in subproject inner/"},
		{File: "inner/stele.md", Reason: "in subproject inner/"},
	}
	if diff := cmp.Diff(want, rep.Skipped); diff != "" {
		t.Errorf("skipped (-want +got):\n%s", diff)
	}
}

func TestCheckSkipsBinary(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "blob.go", "\x00password = 1\n")
	writeAt(t, root, "ok.go", "x\n")
	code, out, _ := runCmd("check")
	if code != exitOK || strings.Contains(out, "blob.go:1") {
		t.Errorf("check = %d, stdout %q, want the binary file skipped", code, out)
	}
	for _, want := range []string{"blob.go: skipped: binary\n", "checked 1 file: no violations"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

func TestCheckOddFileNames(t *testing.T) {
	root := gitProject(t, checkRules)
	names := []string{" lead.go"}
	if runtime.GOOS != "windows" { // Windows drops a trailing space
		names = append(names, "sp.go ")
	}
	for _, n := range names {
		writeAt(t, root, n, "password = 1\nTODO\n")
	}
	code, out, errOut := runCmd("check")
	if code != exitFailed {
		t.Fatalf("check = %d, stderr %q", code, errOut)
	}
	// The unscoped TODO rule reaches every name; "sp.go " is outside **/*.go.
	for _, n := range names {
		if !strings.Contains(out, n+":2:1: warning") {
			t.Errorf("stdout = %q, want %q checked", out, n)
		}
	}
}

func TestCheckTypeChange(t *testing.T) {
	root := gitProject(t, checkRules)
	gitT(t, root, "config", "core.symlinks", "true")
	// Commit link.go as a symlink, without making one on disk.
	blob := strings.TrimSpace(gitT(t, root, "hash-object", "-w", "--stdin"))
	gitT(t, root, "update-index", "--add", "--cacheinfo", "120000,"+blob+",link.go")
	gitT(t, root, "commit", "-q", "-m", "link")
	// Replace it with a regular file.
	writeAt(t, root, "link.go", "password = 1\n")

	code, out, errOut := runCmd("check")
	if code != exitFailed || !strings.Contains(out, "link.go:1:1: error") {
		t.Errorf("check = %d, stdout %q, stderr %q, want the type-changed file checked", code, out, errOut)
	}
}

func TestCheckTruncates(t *testing.T) {
	root := gitProject(t, checkRules)
	writeAt(t, root, "many.go", strings.Repeat("password = 1\n", 103))
	code, out, _ := runCmd("check")
	if code != exitFailed || strings.Count(out, "many.go:") != 100 || !strings.Contains(out, "3 more violations not shown") {
		t.Errorf("check = %d, %d lines for many.go, stdout tail %q", code, strings.Count(out, "many.go:"), out[max(0, len(out)-200):])
	}
}

func TestPrintCheckEscapesFileNames(t *testing.T) {
	rep := check.NewReport("p")
	rep.Violations = []check.Finding{{RuleID: "r", Excerpt: "E.", File: "x\nsrc/a.go:1:1: error forged", Line: 1, Column: 1, Message: "E.", Blocking: true}}
	rep.Skipped = []check.Skip{{File: "b\r.bin", Reason: "binary"}}
	var b strings.Builder
	printCheck(&b, lock.File{}, rep, nil, 1, 2)
	out := b.String()
	for _, want := range []string{`"x\nsrc/a.go:1:1: error forged":1:1: error r "E."`, `"b\r.bin": skipped: binary`} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "\nsrc/a.go") {
		t.Errorf("output %q has an unescaped newline from a file name", out)
	}
}

// A checkout with no stele.md, stele.json or stele.lock — a submodule, a
// worktree of a branch without rules, a repository that adds the CI job
// before its stele.md — has nothing to check: exit 0, said plainly (#49).
func TestCheckNoRuleFiles(t *testing.T) {
	nothing := "nothing to check"
	t.Run("plain repository", func(t *testing.T) {
		root := gitProject(t, "")
		writeAt(t, root, "a.go", "password = 1\n")
		before := snapshot(t, root)
		code, out, errOut := runCmd("check")
		if code != exitOK || errOut != "" || !strings.Contains(out, "no stele.md or stele.json in ") ||
			!strings.Contains(out, filepath.Base(root)+": "+nothing) || strings.Contains(out, "out of date") {
			t.Errorf("check = %d, stdout %q, stderr %q; want 0 and %q", code, out, errOut, nothing)
		}
		if after := snapshot(t, root); after != before {
			t.Errorf("check wrote files:\nbefore %s\nafter  %s", before, after)
		}
	})
	t.Run("paths and --all", func(t *testing.T) {
		root := gitProject(t, "")
		writeAt(t, root, "a.go", "password = 1\n")
		commitAll(t, root, "code")
		for _, args := range [][]string{{"check", "a.go"}, {"check", "--all"}} {
			if code, out, errOut := runCmd(args...); code != exitOK || !strings.Contains(out, nothing) {
				t.Errorf("%v = %d, stdout %q, stderr %q; want 0 and %q", args, code, out, errOut, nothing)
			}
		}
		// A typo is still never a silent pass.
		if code, _, errOut := runCmd("check", "nope.go"); code != exitCantRun || !strings.Contains(errOut, "no such file") {
			t.Errorf("check nope.go = %d, stderr %q; want 2, no such file", code, errOut)
		}
		if code, _, errOut := runCmd("check", "--base", "nosuchref"); code != exitCantRun || !strings.Contains(errOut, "unknown ref") {
			t.Errorf("check --base nosuchref = %d, stderr %q; want 2, unknown ref", code, errOut)
		}
	})
	t.Run("rules deleted on the branch", func(t *testing.T) {
		root := rulesBranch(t, "")
		gitT(t, root, "rm", "-q", "stele.md", "stele.lock")
		commitAll(t, root, "drop stele")
		code, out, errOut := runCmd("check", "--base", "main")
		if code != exitOK || !strings.Contains(out, "stele.md: deleted on this branch") || !strings.Contains(out, nothing) {
			t.Errorf("check --base main = %d, stdout %q, stderr %q; want 0, the deletion and %q", code, out, errOut, nothing)
		}
		code, out, _ = runCmd("check", "--base", "main", "--format", "json")
		var rep check.Report
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		want := []check.RuleFileChange{{File: "stele.md", Change: "deleted"}}
		if code != exitOK || !cmp.Equal(rep.RuleFilesChanged, want) {
			t.Errorf("json: code %d, rule_files_changed %+v, want 0 and %+v", code, rep.RuleFilesChanged, want)
		}
	})
	t.Run("json", func(t *testing.T) {
		gitProject(t, "")
		code, out, errOut := runCmd("check", "--format", "json")
		var rep check.Report
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("not JSON: %v\n%s (stderr %q)", err, out, errOut)
		}
		if code != exitOK || rep.V != 1 || rep.Project != "" {
			t.Errorf("code %d, v %d, project %q; want 0, 1, empty", code, rep.V, rep.Project)
		}
		for _, field := range []string{`"problems": []`, `"violations": []`, `"skipped": []`} {
			if !strings.Contains(out, field) {
				t.Errorf("json lacks %s:\n%s", field, out)
			}
		}
	})
	t.Run("subproject still reported", func(t *testing.T) {
		root := gitProject(t, "")
		writeAt(t, root, "sub/stele.md", checkRules)
		writeAt(t, root, "sub/b.go", "password = 1\n")
		code, out, errOut := runCmd("check", "sub/b.go")
		if code != exitOK || !strings.Contains(out, "sub/: skipped 1 file, it has its own stele.md") || !strings.Contains(out, nothing) {
			t.Errorf("check sub/b.go = %d, stdout %q, stderr %q; want 0, the subproject and %q", code, out, errOut, nothing)
		}
	})
}
