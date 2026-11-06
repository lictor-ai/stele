package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
)

// installPlugin puts this test binary in dir as stele-<kind>-<mode>, the
// fake plugin (plugintest), and puts dir first on PATH.
func installPlugin(t *testing.T, dir, kind, mode string) string {
	t.Helper()
	name := "stele-" + kind + "-" + mode
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, name)
	if err := os.Link(os.Args[0], dst); err != nil {
		data, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dst
}

const pluginRule = `{"body": "Every exported function has a doc comment.", "scope": ["**/*.go"]}`

// withPluginSource lists the source plugin name in stele.json, with rules.
func withPluginSource(t *testing.T, root, name, fp, rules string) {
	t.Helper()
	writeAt(t, root, "stele.json", `{"sources": [{"name": "`+name+`", "config": {"fingerprint": "`+fp+`", "rules": [`+rules+`]}}]}`+"\n")
}

func TestCompileExternalSource(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "good")
	root := project(t, hookRules)
	withPluginSource(t, root, "good", "v1", pluginRule)
	code, out, errOut := runCmd("compile")
	if code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "3 rules") {
		t.Errorf("stdout = %q, want 3 rules", out)
	}
	rulesFile, _ := os.ReadFile(filepath.Join(root, ".claude", "rules", "stele-rules.md"))
	if !strings.Contains(string(rulesFile), "Every exported function has a doc comment.") {
		t.Errorf("rules file lacks the plugin's rule:\n%s", rulesFile)
	}
	f, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Rules[2].Source; got != "good" {
		t.Errorf("lock source = %q, want good", got)
	}

	// stele check reads it again: the same rules pass.
	writeAt(t, root, "a.go", "package a\n")
	if code, out, errOut := runCmd("check", "a.go"); code != exitOK {
		t.Errorf("check = %d, stdout %q, stderr %q", code, out, errOut)
	}
	// A source whose rules changed since compile: the lock is out of date.
	withPluginSource(t, root, "good", "v2", pluginRule+`, {"body": "A second rule."}`)
	if code, out, errOut := runCmd("check", "a.go"); code != exitFailed {
		t.Errorf("check after the source changed = %d, stdout %q, stderr %q; want 1", code, out, errOut)
	}
}

func TestInitRunsExternalSource(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "good")
	root := project(t, hookRules)
	withPluginSource(t, root, "good", "v1", pluginRule)
	if code, _, errOut := runCmd("init"); code != exitOK {
		t.Fatalf("init = %d, stderr %q", code, errOut)
	}
	rulesFile, _ := os.ReadFile(filepath.Join(root, ".claude", "rules", "stele-rules.md"))
	if !strings.Contains(string(rulesFile), "Every exported function has a doc comment.") {
		t.Errorf("rules file lacks the plugin's rule:\n%s", rulesFile)
	}
}

func TestInitNotesAMissingPlugin(t *testing.T) {
	root := project(t, hookRules)
	withPluginSource(t, root, "nothere", "v1", pluginRule)
	code, out, errOut := runCmd("init")
	if code != exitOK || !strings.Contains(out+errOut, "stele-source-nothere not found on PATH") {
		t.Errorf("init = %d, stdout %q, stderr %q; want 0 and a note", code, out, errOut)
	}
}

func TestCompileExternalSourceFailures(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	for _, mode := range []string{"crash", "badproto", "wrongkind", "errors"} {
		installPlugin(t, bin, "source", mode)
	}
	for _, c := range []struct{ name, want string }{
		{"nothere", `stele-source-nothere not found on PATH: install it, or remove "nothere" from "sources" in stele.json`},
		{"crash", "stele-source-crash describe: exited with status 3: the fake plugin crashed"},
		{"badproto", "speaks protocol 2"},
		{"wrongkind", "not a source"},
		{"errors", "stele-source-errors read: the fake plugin was told to fail"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := project(t, hookRules)
			withPluginSource(t, root, c.name, "v1", pluginRule)
			code, _, errOut := runCmd("compile")
			if code != exitCantRun || !strings.Contains(errOut, c.want) {
				t.Errorf("compile = %d, stderr %q; want 2 and %q", code, errOut, c.want)
			}
			writeAt(t, root, "a.go", "package a\n")
			if code, _, errOut := runCmd("check", "a.go"); code != exitCantRun {
				t.Errorf("check = %d, stderr %q; want 2", code, errOut)
			}
		})
	}
}

func TestCompilePluginRefusedInsideProject(t *testing.T) {
	root := project(t, hookRules)
	installPlugin(t, filepath.Join(root, "tools"), "source", "good")
	withPluginSource(t, root, "good", "v1", pluginRule)
	code, _, errOut := runCmd("compile")
	if code != exitCantRun || !strings.Contains(errOut, "refused") || !strings.Contains(errOut, "inside the project") {
		t.Errorf("compile = %d, stderr %q; want 2, refused inside the project", code, errOut)
	}
}

func TestPluginStderrPassedThrough(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "noisy")
	root := project(t, hookRules)
	withPluginSource(t, root, "noisy", "v1", pluginRule)
	code, _, errOut := runCmd("compile")
	if code != exitOK || !strings.Contains(errOut, "stele-source-noisy: line 0") || strings.Contains(errOut, "\x1b") {
		t.Errorf("compile = %d, stderr starts %q", code, errOut[:min(len(errOut), 200)])
	}
}

// The hook and stats never start a plugin (D79), and on a fresh clone the
// hook leaves the rules file, which holds the plugin's rules, as it is (D81).
func TestHookAndStatsNeverRunPlugins(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "witness")
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv("STELE_FAKE_WITNESS", witness)
	root := project(t, hookRules)
	withPluginSource(t, root, "witness", "v1", pluginRule)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	if data, _ := os.ReadFile(witness); !strings.Contains(string(data), "source read") {
		t.Fatalf("witness after compile = %q: the test would not see the plugin run", data)
	}
	if err := os.Remove(witness); err != nil {
		t.Fatal(err)
	}
	rulesFile := filepath.Join(root, ".claude", "rules", "stele-rules.md")
	rulesBefore, _ := os.ReadFile(rulesFile)
	lockBefore, _ := os.ReadFile(filepath.Join(root, lock.FileName))

	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}
	hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	postHook(t, root, "post-write", "a.go", "package a\n")
	runCmd("stats")

	if data, err := os.ReadFile(witness); err == nil {
		t.Errorf("a plugin ran from the hook or stats: %q", data)
	}
	if after, _ := os.ReadFile(rulesFile); string(after) != string(rulesBefore) {
		t.Errorf("the hook rewrote the rules file:\n%s\nwant\n%s", after, rulesBefore)
	}
	if after, _ := os.ReadFile(filepath.Join(root, lock.FileName)); string(after) != string(lockBefore) {
		t.Errorf("the hook rewrote stele.lock:\n%s\nwant\n%s", after, lockBefore)
	}
}

// sources is where plugins start: the walk that checks the hook would see
// it.
func TestSourcesReachPluginexec(t *testing.T) {
	if !declReach(t, "sources")[module+"/internal/pluginexec"] {
		t.Error("sources does not reach internal/pluginexec")
	}
	if got := callReach(t, "runCheck"); !holds(got, module+"/internal/pluginexec") {
		t.Errorf("runCheck reaches %v, want internal/pluginexec", got)
	}
}

func holds(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

const checkedRules = "scope: [\"**/*.go\"]\ncheck: good\n\nRun the linter.\n"

// withPluginChecker lists the checker plugin name in stele.json, answering
// with violations.
func withPluginChecker(t *testing.T, root, name, extra, violations string) {
	t.Helper()
	writeAt(t, root, "stele.json", `{"checkers": [{"name": "`+name+`"`+extra+`, "config": {"violations": [`+violations+`]}}]}`+"\n")
}

// checkedProject is a project whose one rule names the checker good, compiled
// with good listed, and the rule's id.
func checkedProject(t *testing.T) (root, id string) {
	t.Helper()
	root = project(t, checkedRules)
	withPluginChecker(t, root, "good", "", "")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	f, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, root, "a.go", "package a\n")
	return root, f.Rules[0].ID
}

func TestCheckExternalChecker(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "checker", "good")
	root, id := checkedProject(t)
	withPluginChecker(t, root, "good", "", `{"rule_id": "`+id+`", "file": "a.go", "line": 1, "column": 9, "message": "the linter says no"}`)
	code, out, errOut := runCmd("check", "a.go")
	if code != exitFailed || !strings.Contains(out, "a.go:1:9: error "+id) || !strings.Contains(out, "the linter says no") {
		t.Errorf("check = %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, _ = runCmd("check", "--format", "json", "a.go")
	if code != exitFailed || !strings.Contains(out, `"message": "the linter says no"`) {
		t.Errorf("check json = %d, %s", code, out)
	}
	withPluginChecker(t, root, "good", "", "")
	if code, out, errOut := runCmd("check", "a.go"); code != exitOK {
		t.Errorf("clean check = %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestCheckExternalCheckerFailures(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	for _, m := range []string{"good", "crash", "slow", "badjson"} {
		installPlugin(t, bin, "checker", m)
	}
	for _, c := range []struct{ name, extra, want string }{
		{"nothere", "", `stele-checker-nothere not found on PATH: install it, or remove "nothere" from "checkers" in stele.json`},
		{"crash", "", "stele-checker-crash describe: exited with status 3: the fake plugin crashed"},
		{"badjson", "", "answer is not valid JSON"},
	} {
		root := project(t, strings.ReplaceAll(checkedRules, "check: good", "check: "+c.name))
		withPluginChecker(t, root, c.name, c.extra, "")
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("%s: compile: %s", c.name, errOut)
		}
		writeAt(t, root, "a.go", "package a\n")
		code, _, errOut := runCmd("check", "a.go")
		if code != exitCantRun || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: check = %d, stderr %q; want 2 and %q", c.name, code, errOut, c.want)
		}
	}
}

func TestCheckExternalCheckerTimeout(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "checker", "slow")
	root := project(t, strings.ReplaceAll(checkedRules, "check: good", "check: slow"))
	withPluginChecker(t, root, "slow", `, "timeout": "1s"`, "")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	writeAt(t, root, "a.go", "package a\n")
	// describe has its own 10 s; it is the one that times out here.
	code, _, errOut := runCmd("check", "a.go")
	if code != exitCantRun || !strings.Contains(errOut, "no answer in") {
		t.Errorf("check = %d, stderr %q; want 2, no answer", code, errOut)
	}
}

// stele compile and the hook know a listed checker by name: its rules are
// valid, and it never runs (D79).
func TestCompileAndHookNeverRunCheckers(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "checker", "witness")
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv("STELE_FAKE_WITNESS", witness)
	root := project(t, strings.ReplaceAll(checkedRules, "check: good", "check: witness"))
	withPluginChecker(t, root, "witness", "", "")
	code, out, errOut := runCmd("compile")
	if code != exitOK || strings.Contains(out+errOut, "not a checker") {
		t.Fatalf("compile = %d, stdout %q, stderr %q", code, out, errOut)
	}
	hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	post, _ := postHook(t, root, "post-write", "a.go", "package a\n")
	if strings.Contains(post.HookSpecificOutput.AdditionalContext, "not a checker") {
		t.Errorf("the hook calls the rule invalid: %s", post.HookSpecificOutput.AdditionalContext)
	}
	if data, err := os.ReadFile(witness); err == nil {
		t.Fatalf("a checker ran from compile or the hook: %q", data)
	}
	writeAt(t, root, "a.go", "package a\n")
	if code, _, errOut := runCmd("check", "a.go"); code != exitOK {
		t.Fatalf("check: %s", errOut)
	}
	if data, _ := os.ReadFile(witness); !strings.Contains(string(data), "checker validate") || !strings.Contains(string(data), "checker check") {
		t.Errorf("witness after check = %q: want validate and check", data)
	}
}

// A rule naming a checker stele.json doesn't list is still invalid.
func TestCompileUnlistedCheckerIsInvalid(t *testing.T) {
	root := project(t, checkedRules)
	code, out, errOut := runCmd("compile")
	if !strings.Contains(out+errOut, `"good" is not a checker`) {
		t.Errorf("compile = %d, stdout %q, stderr %q; want the rule called invalid", code, out, errOut)
	}
	_ = root
}
