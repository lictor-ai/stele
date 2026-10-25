package functional

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const violating = "package a\n\nfunc f() { fmt.Println(1) }\n"

// stele check with no arguments checks what changed against HEAD: staged,
// unstaged and untracked. A committed violation is not reported; --all
// reports it.
func TestCheckChangedFiles(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.write("old.go", violating)
	p.commit()

	out := p.mustStele(0, "check")
	if strings.Contains(out, "old.go") {
		t.Errorf("a committed, unchanged file was checked:\n%s", out)
	}
	p.write("untracked.go", violating)
	p.write("staged.go", violating)
	p.git("add", "staged.go")
	p.write("old.go", violating+"\nfunc g() {}\n") // unstaged change
	out = p.mustStele(1, "check")
	for _, f := range []string{"untracked.go", "staged.go", "old.go"} {
		if !strings.Contains(out, f+":") {
			t.Errorf("stele check missed %s:\n%s", f, out)
		}
	}
	out = p.mustStele(1, "check", "--all")
	if !strings.Contains(out, "old.go:") {
		t.Errorf("--all missed a tracked file:\n%s", out)
	}
}

// --base checks the branch's changes since the merge base, deleted files are
// skipped, and renamed files are checked under their new name.
func TestCheckBase(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.write("keep.go", "package a\n")
	p.write("doomed.go", violating)
	p.write("old_name.go", "package a\n")
	p.commit()
	p.git("branch", "-M", "main")
	p.git("checkout", "-q", "-b", "feature")
	p.git("rm", "-q", "doomed.go")
	p.git("mv", "old_name.go", "new_name.go")
	p.write("new_name.go", violating)
	p.write("dir with space/ünïcode.go", violating)
	p.commit()
	// main moves on with its own violation: not this branch's problem.
	p.git("checkout", "-q", "main")
	p.write("main_only.go", violating)
	p.commit()
	p.git("checkout", "-q", "feature")

	out := p.mustStele(1, "check", "--base", "main")
	if strings.Contains(out, "doomed.go") || strings.Contains(out, "main_only.go") {
		t.Errorf("--base checked a deleted file or a file changed only on main:\n%s", out)
	}
	for _, f := range []string{"new_name.go", "ünïcode.go"} {
		if !strings.Contains(out, f) {
			t.Errorf("--base missed %s:\n%s", f, out)
		}
	}
	if code, _, _ := p.stele("check", "--base", "no-such-ref"); code != 2 {
		t.Errorf("unknown --base ref: exit %d, want 2", code)
	}
}

// The lock: missing from git, or stale after editing stele.md, fails check.
// CI sees a fresh clone, so the test checks one.
func TestCheckLock(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.git("add", "stele.md")
	p.git("commit", "-q", "-m", "rules only")
	clone := func() *project {
		dir := t.TempDir()
		p.git("clone", "-q", p.dir, dir)
		return &project{t: t, dir: dir, env: p.env}
	}
	if code, out, errOut := clone().stele("check", "--all"); code != 1 {
		t.Errorf("clone without stele.lock: exit %d, want 1\n%s%s", code, out, errOut)
	}
	p.commit()
	clone().mustStele(0, "check", "--all")
	p.write("stele.md", goRules+"\n---\n\nA new rule.\n")
	p.git("add", "stele.md")
	p.git("commit", "-q", "-m", "new rule, lock not updated")
	if code, out, errOut := clone().stele("check", "--all"); code != 1 {
		t.Errorf("stele.lock stale after a new rule: exit %d, want 1\n%s%s", code, out, errOut)
	}
}

// --format json is a stable, versioned contract (D49): it must parse, carry
// v, and agree with the text output.
func TestCheckJSON(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.write("a.go", violating+"var k = \"AKIAIOSFODNN7EXAMPLE\"\n")
	p.commit()
	code, out, _ := p.stele("check", "--all", "--format", "json")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	var r struct {
		V          int    `json:"v"`
		Project    string `json:"project"`
		Violations []struct {
			File     string `json:"file"`
			Line     int    `json:"line"`
			RuleID   string `json:"rule_id"`
			Blocking bool   `json:"blocking"`
		} `json:"violations"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if r.V != 1 || r.Project == "" || len(r.Violations) != 2 {
		t.Errorf("want v=1, a project id and 2 violations:\n%s", out)
	}
}

// A subproject is skipped by the parent's check, and said to be.
func TestCheckSkipsSubprojects(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.write("sub/stele.md", "Sub rule.\n")
	p.write("sub/a.go", violating)
	p.commit()
	code, out, errOut := p.stele("check", "--all")
	if strings.Contains(out, "sub/a.go:") {
		t.Errorf("the parent checked a subproject's file against its own rules:\n%s", out)
	}
	if !strings.Contains(out+errOut, "sub") {
		t.Errorf("the skip of sub/ was silent (exit %d):\n%s%s", code, out, errOut)
	}
}

// Outside a git repository, check cannot know what changed.
func TestCheckNoGit(t *testing.T) {
	p := newProject(t, goRules)
	p.git("init", "-q") // no-op, keeps helper happy
	p.compile()
	dir := t.TempDir()
	q := &project{t: t, dir: dir, env: p.env}
	q.write("stele.md", goRules)
	q.compile()
	if code, _, _ := q.stele("check"); code != 2 {
		t.Errorf("no git: exit %d, want 2", code)
	}
}

// Check over a large repository: time it.
func TestCheckScale(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	p := newProject(t, goRules)
	p.compile()
	const files = 2000
	for i := range files {
		body := "package a\n\nfunc f() int { return 1 }\n"
		if i%100 == 0 {
			body = violating
		}
		p.write(fmt.Sprintf("pkg%d/f%d.go", i/50, i), body)
	}
	p.commit()
	start := time.Now()
	out := p.mustStele(1, "check", "--all")
	d := time.Since(start)
	if n := strings.Count(out, ": error "); n != files/100 {
		t.Errorf("found %d violations in %d files, want %d", n, files, files/100)
	}
	measure("check_all_2000_files_ms", d.Milliseconds())
}
