package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	gitlink(t, root, "vendor/lib")
	if _, out := hook(t, write(filepath.Join(root, "vendor", "lib", "x.go"), "p1", "")); out != "" {
		t.Errorf("hook in a submodule said %q, want nothing", out)
	}
}

// A directory holding .git is a repository root, a worktree's too, whatever
// is above it (#42).
func TestInitInAWorktree(t *testing.T) {
	root := project(t, "")
	sub := filepath.Join(root, "sub")
	gitlink(t, root, "sub")
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
	gitlink(t, root, "lib")
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
	gitlink(t, root, "vendor/lib")
	writeAt(t, root, "vendor/lib/x.go", "password = 1\n")
	t.Chdir(filepath.Join(root, "vendor", "lib"))
	code, out, errOut := runCmd("check", "x.go")
	if code != exitOK || !strings.Contains(out, "nothing to check") || strings.Contains(out, "x.go:1:1") {
		t.Errorf("check in a submodule = %d, stdout %q, stderr %q; want 0 and nothing to check", code, out, errOut)
	}
}

// A .git folder git doesn't recognise, as one write into src/.git/ leaves,
// ends no project: the files beside it are still the outer project's, as
// git lists them, so the hook still denies, checks and logs them.
func TestHookKeepsRulesBesideAGitGitIgnores(t *testing.T) {
	clock(t, time.Now())
	root := project(t, checkRules)
	writeAt(t, root, "src/.git/x", "")

	o, out := hook(t, write(filepath.Join(root, "src", "b.go"), "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "src/b.go") {
		t.Errorf("write beside src/.git: %s, want a deny naming src/b.go", out)
	}
	if o, _ := postHook(t, root, "post-write", "src/b.go", "package src\n\nvar password = 1\n"); o.Decision != "block" || !strings.Contains(o.Reason, "src/b.go:3:5:") {
		t.Errorf("check beside src/.git: decision %q, reason %q; want block on src/b.go:3:5", o.Decision, o.Reason)
	}
	log, err := os.ReadFile(filepath.Join(root, ".stele", "log.jsonl"))
	if err != nil || !strings.Contains(string(log), "src/b.go") {
		t.Errorf("log.jsonl names no src/b.go (err %v):\n%s", err, log)
	}
	if _, err := os.Lstat(filepath.Join(root, "src", ".stele")); err == nil {
		t.Error("the hook took src/ for a project of its own")
	}
}

// A write that makes or changes a .git below the root asks: one git
// recognises would end the project, and its rules with it. Inside the
// root's own repository, git's own files, it does not: stele.md beside it
// keeps the project.
func TestHookAsksForGit(t *testing.T) {
	clock(t, time.Now())
	for _, rel := range []string{"src/.git", "src/.git/x", "src/.git/HEAD", ".git", "src/.git/sub/.git/x", ".git/modules/x/.git"} {
		t.Run(rel, func(t *testing.T) {
			root := project(t, hookRules)
			o, out := hook(t, write(filepath.Join(root, filepath.FromSlash(rel)), "p1", ""))
			if !asks(t, o, rel) || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "or a .git") {
				t.Errorf("a write to %s: %s, want an ask", rel, out)
			}
		})
	}
	for _, rel := range []string{".gitignore", "src/.gitattributes", "x.git/y.go", ".github/workflows/ci.yml", ".git/config", ".git/hooks/pre-commit", ".git/index.lock", ".git/info/exclude"} {
		t.Run("not "+rel, func(t *testing.T) {
			root := project(t, hookRules)
			if o, _ := hook(t, write(filepath.Join(root, filepath.FromSlash(rel)), "p1", "")); o.HookSpecificOutput.PermissionDecision == "ask" {
				t.Errorf("asked for %s", rel)
			}
		})
	}
	// The root's .git counts as git's own only when git recognises it.
	t.Run("a root .git git doesn't recognise", func(t *testing.T) {
		root := project(t, hookRules)
		if err := os.Remove(filepath.Join(root, ".git", "HEAD")); err != nil {
			t.Fatal(err)
		}
		if o, out := hook(t, write(filepath.Join(root, ".git", "HEAD"), "p1", "")); !asks(t, o, ".git/HEAD") {
			t.Errorf("a write to .git/HEAD beside no repository: %s, want an ask", out)
		}
	})
}

// A Bash command that writes into a .git asks, as a Write does.
func TestHookBashAsksForGit(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	for command, rel := range map[string]string{
		"echo > src/.git/HEAD":              "src/.git/HEAD",
		"echo 'gitdir: ../.git' > src/.git": "src/.git",
	} {
		o, out := hook(t, bash(command, root, "p1", ""))
		if !asks(t, o, rel) {
			t.Errorf("%s: %s, want an ask", command, out)
		}
	}
	if o, out := hook(t, bash("echo x > .git/info/exclude", root, "p1", "")); o.HookSpecificOutput.PermissionDecision == "ask" {
		t.Errorf("a write into the root's own repository asked: %s", out)
	}
}

// A .git under its 8.3 short name asks too.
func TestHookAsksForGitShortName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("8.3 short names are Windows only")
	}
	clock(t, time.Now())
	root := project(t, hookRules)
	writeAt(t, root, "src/.git/x", "")
	short := filepath.Join(root, "src", "GIT~1")
	if _, err := os.Stat(short); err != nil {
		t.Skipf("no 8.3 short name for src/.git on this volume: %v", err)
	}
	o, out := hook(t, write(filepath.Join(short, "HEAD"), "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("a write to src/GIT~1/HEAD: %s, want an ask", out)
	}
	// The root's own repository under its short name stays git's.
	if rootShort := filepath.Join(root, "GIT~1"); func() bool { _, err := os.Stat(rootShort); return err == nil }() {
		if o, out := hook(t, write(filepath.Join(rootShort, "config"), "p1", "")); o.HookSpecificOutput.PermissionDecision == "ask" {
			t.Errorf("a write to GIT~1/config in the root's repository asked: %s", out)
		}
	}
}

// stele check, as git, takes the files beside a .git git doesn't recognise
// for the outer project's.
func TestCheckBesideAGitGitIgnores(t *testing.T) {
	root := project(t, checkRules)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	writeAt(t, root, "lib/.git/x", "")
	writeAt(t, root, "lib/a.go", "password = 1\n")

	code, out, errOut := runCmd("check", "lib/a.go")
	if code != exitFailed || !strings.Contains(out, "lib/a.go:1:1: error") || strings.Contains(out, "skipped") {
		t.Errorf("check = %d, stdout %q, stderr %q; want lib/a.go checked", code, out, errOut)
	}
}
