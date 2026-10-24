package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree on a branch without stele.md gets no rules from the main
// checkout's: its paths would start with .claude/worktrees/<id>/ and miss
// every scope written for the project (#43).
func TestHookIgnoresOuterRulesInWorktree(t *testing.T) {
	main := gitProject(t, "")
	wt := worktree(t, main, "wt1")
	writeAt(t, main, "stele.md", hookRules)
	file := filepath.Join(wt, "internal", "db", "query.go")

	if _, out := hook(t, write(file, "p1", "")); out != "" {
		t.Errorf("hook in a worktree without stele.md said %q, want nothing", out)
	}
	for _, dir := range []string{main, wt} {
		if _, err := os.Lstat(filepath.Join(dir, ".stele")); err == nil {
			t.Errorf("hook created %s", filepath.Join(dir, ".stele"))
		}
	}

	// With its own stele.md, the worktree is the root, and paths are its own.
	writeAt(t, wt, "stele.md", hookRules)
	o, _ := hook(t, write(file, "p2", ""))
	reason := o.HookSpecificOutput.PermissionDecisionReason
	if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(reason, "internal/db/query.go") || strings.Contains(reason, ".claude/worktrees") {
		t.Errorf("hook in a worktree with stele.md: decision %q, reason %q", o.HookSpecificOutput.PermissionDecision, reason)
	}
}

// A submodule is another repository: the outer rules don't reach into it,
// as the outer stele check never sees its files.
func TestHookIgnoresOuterRulesInSubmodule(t *testing.T) {
	root := project(t, hookRules)
	writeAt(t, root, "vendor/lib/.git", "gitdir: ../../.git/modules/lib\n")
	if _, out := hook(t, write(filepath.Join(root, "vendor", "lib", "x.go"), "p1", "")); out != "" {
		t.Errorf("hook in a submodule said %q, want nothing", out)
	}
}

// A directory holding .git is a repository root, a worktree's too, whatever
// is above it (#42).
func TestInitInAWorktree(t *testing.T) {
	root := project(t, "")
	sub := filepath.Join(root, "sub")
	writeAt(t, root, "sub/.git", "gitdir: x\n")
	t.Chdir(sub)

	for _, cmd := range []string{"init", "uninstall"} {
		code, _, errOut := runCmd(cmd)
		if code != exitOK {
			t.Fatalf("%s = %d, stderr %q", cmd, code, errOut)
		}
		if strings.Contains(errOut, "not the repository root") {
			t.Errorf("%s in a worktree: stderr %q, want no note about the root", cmd, errOut)
		}
		if cmd == "init" {
			if _, err := os.Stat(filepath.Join(sub, ".claude", "settings.json")); err != nil {
				t.Errorf("init did not set up the worktree: %v", err)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude")); err == nil {
		t.Error("init wrote to the outer checkout")
	}
}

func TestInitInAGitWorktree(t *testing.T) {
	main := gitProject(t, "")
	wt := worktree(t, main, "wt1")

	t.Chdir(wt)
	if code, _, errOut := runCmd("init"); code != exitOK || strings.Contains(errOut, "not the repository root") {
		t.Errorf("init in a worktree = %d, stderr %q, want no note about the root", code, errOut)
	}

	// Below the worktree's root, the note names the worktree, not main.
	deep := filepath.Join(wt, "deep")
	if err := os.Mkdir(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)
	_, _, errOut := runCmd("init")
	if !strings.Contains(errOut, "not the repository root ("+wt+")") {
		t.Errorf("init below a worktree: stderr %q, want the note to name %s", errOut, wt)
	}
}

// stele check at the outer root leaves a nested checkout to that checkout,
// and says so.
func TestCheckSkipsNestedRepositories(t *testing.T) {
	root := project(t, checkRules)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	writeAt(t, root, "lib/.git", "gitdir: x\n")
	writeAt(t, root, "lib/a.go", "password = 1\n")
	writeAt(t, root, "b.go", "password = 2\n")

	code, out, errOut := runCmd("check", "lib/a.go", "b.go")
	if code != exitFailed {
		t.Fatalf("check = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "b.go:1:1: error") || strings.Contains(out, "lib/a.go:1:1") || !strings.Contains(out, "lib/: skipped 1 file, it has its own .git") {
		t.Errorf("stdout = %q", out)
	}

	_, out, _ = runCmd("check", "--format", "json", "lib/a.go", "b.go")
	if !strings.Contains(out, `"file": "lib/a.go"`) || !strings.Contains(out, `"reason": "in subproject lib/"`) {
		t.Errorf("json = %s, want lib/a.go skipped as in subproject lib/", out)
	}
}

// git lists an untracked nested repository as one directory entry,
// nested/: the skip names it without counting it as a file.
func TestCheckUntrackedNestedRepository(t *testing.T) {
	root := gitProject(t, checkRules)
	nested := filepath.Join(root, "nested")
	writeAt(t, root, "nested/a.go", "password = 1\n")
	gitT(t, nested, "init", "-q")

	code, out, errOut := runCmd("check")
	if code != exitOK {
		t.Fatalf("check = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "nested/: skipped, it has its own .git\n") || strings.Contains(out, "skipped 1 file") {
		t.Errorf("stdout = %q, want nested/ skipped without a file count", out)
	}
}

// A worktree without rules is its own root: the main checkout's rules are
// not applied to it, and the check passes cleanly.
func TestCheckInWorktree(t *testing.T) {
	main := gitProject(t, checkRules)
	wt := worktree(t, main, "wt2")
	for _, name := range []string{"stele.md", "stele.lock"} {
		if err := os.Remove(filepath.Join(wt, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(t, wt, "a.go", "password = 1\n")

	// No rule files at all: nothing to check (#49).
	t.Chdir(wt)
	code, out, errOut := runCmd("check", "a.go")
	if code != exitOK || errOut != "" || strings.Contains(out, "a.go:1:1") || !strings.Contains(out, "nothing to check") {
		t.Errorf("stele check in a worktree without rules = %d, stdout %q, stderr %q; want 0, nothing to check, nothing on stderr", code, out, errOut)
	}

	// Compiled, it is a stele project with no rules.
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile in the worktree = %d, stderr %q", code, errOut)
	}
	code, out, errOut = runCmd("check", "a.go")
	if code != exitOK || errOut != "" || strings.Contains(out, "a.go:1:1") || !strings.Contains(out, "0 rules valid") {
		t.Errorf("stele check after compile = %d, stdout %q, stderr %q; want 0, 0 rules valid", code, out, errOut)
	}
}

// A submodule without rules has nothing to check, from inside it.
func TestCheckInSubmoduleWithoutRules(t *testing.T) {
	root := project(t, checkRules)
	writeAt(t, root, "vendor/lib/.git", "gitdir: x\n")
	writeAt(t, root, "vendor/lib/x.go", "password = 1\n")
	t.Chdir(filepath.Join(root, "vendor", "lib"))
	code, out, errOut := runCmd("check", "x.go")
	if code != exitOK || !strings.Contains(out, "nothing to check") || strings.Contains(out, "x.go:1:1") {
		t.Errorf("check in a submodule = %d, stdout %q, stderr %q; want 0 and nothing to check", code, out, errOut)
	}
}
