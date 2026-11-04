package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
)

const sharedRepo = "git@github.com:acme/guidelines.git"

const sharedRules = "Wrap every error with the context it lost.\n\n---\n\nscope: [\"**/*.go\"]\n\nNo panic in library code.\n"

// identity lets a test commit with no git config of the user's.
var identity = []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(identity, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sharedRemote makes a bare guidelines repository holding stele.md on
// main, points the git source at it for this test, and gives the source a
// cache of its own. It returns the bare repository, a work clone and the
// first commit.
func sharedRemote(t *testing.T) (bare, work, first string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	bare = filepath.Join(t.TempDir(), "guidelines.git")
	gitIn(t, filepath.Dir(bare), "init", "--bare", "--quiet", "--initial-branch=main", bare)
	work = t.TempDir()
	gitIn(t, work, "init", "--quiet", "--initial-branch=main")
	writeAt(t, work, "stele.md", sharedRules)
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "--quiet", "-m", "rules")
	gitIn(t, work, "push", "--quiet", bare, "main")
	first = gitIn(t, work, "rev-parse", "HEAD")

	cache := t.TempDir()
	oldCache, oldRemotes := gitCacheDir, localRemotes
	gitCacheDir = func() (string, error) { return cache, nil }
	localRemotes = map[string]string{sharedRepo: bare}
	t.Cleanup(func() { gitCacheDir, localRemotes = oldCache, oldRemotes })
	return bare, work, first
}

// pushShared commits a new stele.md to the guidelines and returns its
// commit.
func pushShared(t *testing.T, work, bare, content string) string {
	t.Helper()
	writeAt(t, work, "stele.md", content)
	gitIn(t, work, "commit", "--quiet", "-am", "more")
	gitIn(t, work, "push", "--quiet", bare, "main")
	return gitIn(t, work, "rev-parse", "HEAD")
}

func withGitSource(t *testing.T, root, ref string) {
	t.Helper()
	cfg := `{"repo": "` + sharedRepo + `"`
	if ref != "" {
		cfg += `, "ref": "` + ref + `"`
	}
	writeAt(t, root, "stele.json", `{"sources": [{"name": "git", "config": `+cfg+`}}]}`+"\n")
}

func TestCompileGitSource(t *testing.T) {
	bare, work, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")

	code, out, errOut := runCmd("compile")
	if code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"github.com/acme/guidelines/stele.md: pinned main at " + first[:7],
		"4 rules: 0 matched by text, 0 by position, 4 new, 0 dropped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	f, err := lock.Load(root)
	if err != nil || len(f.Sources) != 1 || f.Sources[0].Commit != first {
		t.Fatalf("stele.lock = %+v, %v; want the pin of %s", f, err, first)
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude", "rules", "stele-rules.md"))
	if err != nil || !strings.Contains(string(data), "No panic in library code.") {
		t.Errorf("rules file lacks the shared rules:\n%s", data)
	}

	// A new commit on main does not move the pin, and the cache serves it
	// with the remote gone.
	second := pushShared(t, work, bare, sharedRules+"\n---\n\nName every goroutine's owner.\n")
	localRemotes[sharedRepo] = filepath.Join(t.TempDir(), "gone.git")
	code, out, errOut = runCmd("compile")
	if code != exitOK || !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("compile offline = %d, stdout %q, stderr %q; want up to date", code, out, errOut)
	}

	// --update moves it.
	localRemotes[sharedRepo] = bare
	code, out, errOut = runCmd("compile", "--update")
	if code != exitOK || !strings.Contains(out, "main "+first[:7]+" -> "+second[:7]) || !strings.Contains(out, "1 new") {
		t.Errorf("compile --update = %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, _ = runCmd("compile", "--update")
	if code != exitOK || !strings.Contains(out, "main already at "+second[:7]) {
		t.Errorf("second compile --update = %d, stdout %q", code, out)
	}
}

func TestCompileUpdateWithNoGitSource(t *testing.T) {
	project(t, rules)
	code, out, _ := runCmd("compile", "--update")
	if code != exitOK || !strings.Contains(out, "no git source in stele.json: nothing to update") {
		t.Errorf("compile --update = %d, stdout %q", code, out)
	}
}

func TestCompileBadSources(t *testing.T) {
	root := project(t, rules)
	writeAt(t, root, "stele.json", `{"sources": [{"name": "git", "config": {"repo": "ext::sh -c calc"}}]}`)
	code, _, errOut := runCmd("compile")
	if code != exitCantRun || !strings.Contains(errOut, "sources[0]") {
		t.Errorf("compile = %d, stderr %q; want exit 2 naming sources[0]", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, lock.FileName)); err == nil {
		t.Error("compile wrote stele.lock")
	}
}

func TestCheckGitSource(t *testing.T) {
	bare, work, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "")
	writeAt(t, root, "main.go", "package main\n")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	if code, out, errOut := runCmd("check", "main.go"); code != exitOK || !strings.Contains(out, "4 rules valid") {
		t.Errorf("check = %d, stdout %q, stderr %q", code, out, errOut)
	}

	// check never moves the pin, even with a newer commit on the ref.
	pushShared(t, work, bare, "Something else entirely.\n")
	if code, _, errOut := runCmd("check", "main.go"); code != exitOK {
		t.Errorf("check after a push = %d, stderr %q", code, errOut)
	}
	if f, _ := lock.Load(root); f.Sources[0].Commit != first {
		t.Errorf("check moved the pin to %s", f.Sources[0].Commit)
	}

	// stele.json changed and stele compile has not run.
	withGitSource(t, root, "main")
	code, _, errOut := runCmd("check", "main.go")
	if code != exitFailed || !strings.Contains(errOut, "stele.lock is out of date with stele.json") {
		t.Errorf("check after a stele.json edit = %d, stderr %q", code, errOut)
	}
	withGitSource(t, root, "")

	// A machine with no cache and no access cannot run the check.
	cache := t.TempDir()
	gitCacheDir = func() (string, error) { return cache, nil }
	localRemotes[sharedRepo] = filepath.Join(t.TempDir(), "gone.git")
	code, _, errOut = runCmd("check", "main.go")
	if code != exitCantRun || !strings.Contains(errOut, "read access to "+sharedRepo) {
		t.Errorf("check with no access = %d, stderr %q", code, errOut)
	}
}

// A pin edited by hand reads other rules than stele.lock holds: check fails.
func TestCheckHandEditedPin(t *testing.T) {
	bare, work, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")
	writeAt(t, root, "main.go", "package main\n")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	second := pushShared(t, work, bare, "Something else entirely.\n")
	data, err := os.ReadFile(filepath.Join(root, lock.FileName))
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, root, lock.FileName, strings.Replace(string(data), first, second, 1))
	code, _, errOut := runCmd("check", "main.go")
	if code != exitFailed || !strings.Contains(errOut, "out of date") {
		t.Errorf("check with an edited pin = %d, stderr %q", code, errOut)
	}
}

// The guidelines' own stele.lock gives the shared rules their ids, the same
// in every project.
func TestGitSourceIDsFromTheGuidelines(t *testing.T) {
	bare, work, _ := sharedRemote(t)
	root := project(t, rules) // first: it points keyDir at a temp directory
	// Compile the guidelines repository itself, as its owner would.
	t.Chdir(work)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile in the guidelines: %s", errOut)
	}
	theirs, err := lock.Load(work)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "stele.lock")
	gitIn(t, work, "commit", "--quiet", "-m", "ids")
	gitIn(t, work, "push", "--quiet", bare, "main")

	t.Chdir(root)
	withGitSource(t, root, "main")
	code, out, errOut := runCmd("compile")
	if code != exitOK || !strings.Contains(out, "2 supplied by their source") {
		t.Fatalf("compile = %d, stdout %q, stderr %q", code, out, errOut)
	}
	ours, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range theirs.Rules {
		if got := ours.Rules[2+i].ID; got != e.ID {
			t.Errorf("shared rule %d id = %s, want the guidelines' %s", i, got, e.ID)
		}
	}
}

func TestInitFetchesGitSource(t *testing.T) {
	_, _, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")
	code, _, errOut := runCmd("init")
	if code != exitOK {
		t.Fatalf("init = %d, stderr %q", code, errOut)
	}
	f, err := lock.Load(root)
	if err != nil || len(f.Sources) != 1 || f.Sources[0].Commit != first {
		t.Errorf("stele.lock after init = %+v, %v", f, err)
	}
}

// A fresh clone has no index. The hook rebuilds from stele.md alone, and
// must leave the committed stele.lock and rules file as they are: they hold
// the shared rules and their pin, which the hook cannot read (D71).
func TestHookOnAFreshCloneKeepsSharedRules(t *testing.T) {
	_, _, _ = sharedRemote(t)
	root := project(t, hookRules)
	withGitSource(t, root, "main")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	rulesFile := filepath.Join(root, ".claude", "rules", "stele-rules.md")
	lockBefore, _ := os.ReadFile(filepath.Join(root, lock.FileName))
	rulesBefore, _ := os.ReadFile(rulesFile)
	if !strings.Contains(string(rulesBefore), "No panic in library code.") {
		t.Fatalf("rules file lacks the shared rules:\n%s", rulesBefore)
	}

	// The clone: no .stele/, and the hook must not reach the remote.
	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}
	localRemotes[sharedRepo] = filepath.Join(t.TempDir(), "gone.git")
	hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	postHook(t, root, "post-write", "a.go", "package a\n")

	if after, _ := os.ReadFile(filepath.Join(root, lock.FileName)); string(after) != string(lockBefore) {
		t.Errorf("the hook rewrote stele.lock:\n%s\nwant\n%s", after, lockBefore)
	}
	if after, _ := os.ReadFile(rulesFile); string(after) != string(rulesBefore) {
		t.Errorf("the hook rewrote the rules file:\n%s\nwant\n%s", after, rulesBefore)
	}
	if code, out, errOut := runCmd("compile"); code != exitOK || !strings.Contains(out, "up to date") && !strings.Contains(out, ".stele/index.json") {
		t.Errorf("compile after = %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// sources is where git starts; the hook and stats reach neither it nor
// internal/gitexec.
func TestSourcesReachGit(t *testing.T) {
	if !declReach(t, "sources")[module+"/internal/gitexec"] {
		t.Error("sources does not reach internal/gitexec: the reach tests would not catch the hook using it")
	}
	if declReach(t, "runStats")[module+"/internal/gitexec"] {
		t.Error("runStats reaches internal/gitexec")
	}
}
