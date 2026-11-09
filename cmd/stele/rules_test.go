package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
)

func rulesJSON(t *testing.T) rulesReport {
	t.Helper()
	code, out, errOut := runCmd("rules", "--format", "json")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	var rep rulesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	return rep
}

func TestRulesJSON(t *testing.T) {
	root := compiled(t, rules)
	rep := rulesJSON(t)
	if rep.V != 1 || rep.Project != projectID(t, root) {
		t.Errorf("v = %d, project = %q; want 1, %q", rep.V, rep.Project, projectID(t, root))
	}
	if len(rep.Rules) != 2 {
		t.Fatalf("rules = %+v, want 2", rep.Rules)
	}
	for i, want := range []string{"Never build SQL by string concatenation.", "Every handler goes through the middleware chain."} {
		r := rep.Rules[i]
		if r.ID != ruleID(t, root, i) {
			t.Errorf("rule %d id = %q, want %q", i, r.ID, ruleID(t, root, i))
		}
		if r.Source != (ruleSource{Name: "stelemd", Path: "stele.md"}) {
			t.Errorf("rule %d source = %+v", i, r.Source)
		}
		if r.Scope == nil || len(r.Scope) != 0 {
			t.Errorf("rule %d scope = %#v, want []", i, r.Scope)
		}
		if r.Body != want {
			t.Errorf("rule %d body = %q, want %q", i, r.Body, want)
		}
	}
	if rep.Notes == nil || len(rep.Notes) != 0 {
		t.Errorf("notes = %#v, want []", rep.Notes)
	}

	// The lists are lists in the JSON, never null.
	_, out, _ := runCmd("rules", "--format", "json")
	if !strings.Contains(out, `"scope": []`) || !strings.Contains(out, `"notes": []`) {
		t.Errorf("stdout = %s, want empty lists as []", out)
	}
}

func TestRulesText(t *testing.T) {
	root := compiled(t, "scope: [\"**/*.go\", \"!**/*_test.go\"]\n\nNever build SQL by string concatenation.\nNot even in tests.\n\n---\n\nEvery handler goes through the middleware chain.\n")
	code, out, errOut := runCmd("rules")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	want := ruleID(t, root, 0) + "  stele.md  **/*.go, !**/*_test.go\n" +
		"  Never build SQL by string concatenation. Not even in tests.\n" +
		ruleID(t, root, 1) + "  stele.md  every file\n" +
		"  Every handler goes through the middleware chain.\n" +
		"2 rules\n"
	if out != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out, want)
	}
}

// With no index yet, stele rules compiles the built-in sources, as the hook
// does, and the ids it prints are the ones stele.lock keeps.
func TestRulesCompilesWhenStale(t *testing.T) {
	root := project(t, rules)
	rep := rulesJSON(t)
	if len(rep.Rules) != 2 || rep.Rules[0].ID != ruleID(t, root, 0) || rep.Rules[1].ID != ruleID(t, root, 1) {
		t.Fatalf("rules = %+v, want stele.lock's ids", rep.Rules)
	}
	writeAt(t, root, "stele.md", rules+"\n---\n\nLog with the request id.\n")
	first := rulesJSON(t)
	if len(first.Rules) != 3 {
		t.Fatalf("rules = %+v, want 3", first.Rules)
	}
	second := rulesJSON(t)
	if first.Rules[2].ID != second.Rules[2].ID || first.Rules[2].ID != ruleID(t, root, 2) {
		t.Errorf("new rule id = %q then %q, want stele.lock's %q", first.Rules[2].ID, second.Rules[2].ID, ruleID(t, root, 2))
	}
}

func TestRulesNeverWritesRuleFiles(t *testing.T) {
	root := project(t, rules)
	writeAt(t, root, "stele.json", "{}\n")
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	md, js := read("stele.md"), read("stele.json")
	rulesJSON(t)
	if code, _, errOut := runCmd("rules"); code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	if read("stele.md") != md || read("stele.json") != js {
		t.Error("stele rules changed stele.md or stele.json")
	}
}

// A stele.md that stops parsing leaves the last good rules in force, as in
// the hook, and stele rules says so.
func TestRulesParseErrorFallsBack(t *testing.T) {
	root := compiled(t, rules)
	writeAt(t, root, "stele.md", "scope: [\n\nbroken\n")
	code, out, errOut := runCmd("rules", "--format", "json")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	var rep rulesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 2 || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "last valid rules") {
		t.Errorf("rules = %+v, notes = %q; want the 2 last rules and a note", rep.Rules, rep.Notes)
	}
	if !strings.HasPrefix(errOut, "stele rules: ") || !strings.Contains(errOut, "last valid rules") {
		t.Errorf("stderr = %q, want the note", errOut)
	}
}

func TestRulesNoRuleFiles(t *testing.T) {
	root := project(t, "")
	rep := rulesJSON(t)
	if rep.V != 1 || rep.Project != "" || rep.Rules == nil || len(rep.Rules) != 0 {
		t.Errorf("report = %+v, want an empty list", rep)
	}
	code, out, _ := runCmd("rules")
	if code != exitOK || !strings.Contains(out, "no stele.md or stele.json") {
		t.Errorf("rules = %d, stdout %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); !os.IsNotExist(err) {
		t.Errorf(".stele: %v, want none written", err)
	}
}

func TestRulesCantRun(t *testing.T) {
	compiled(t, rules)
	for _, args := range [][]string{
		{"rules", "--format", "yaml"},
		{"rules", "extra"},
		{"rules", "--nope"},
	} {
		code, out, errOut := runCmd(args...)
		if code != exitCantRun || out != "" || !strings.Contains(errOut, "stele rules") {
			t.Errorf("%v = %d, stdout %q, stderr %q; want 2", args, code, out, errOut)
		}
	}
}

func TestRulesGitSource(t *testing.T) {
	_, _, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	// stele rules starts no process: with the remote gone it still lists
	// the shared rules, from the index.
	localRemotes[sharedRepo] = filepath.Join(t.TempDir(), "gone.git")

	rep := rulesJSON(t)
	if len(rep.Rules) != 4 {
		t.Fatalf("rules = %+v, want 4", rep.Rules)
	}
	shared := rep.Rules[2]
	want := ruleSource{Name: "git:github.com/acme/guidelines/stele.md", Repo: sharedRepo, Ref: "main", Path: "stele.md", Commit: first}
	if shared.Source != want {
		t.Errorf("shared source = %+v, want %+v", shared.Source, want)
	}
	if shared.Body != "Wrap every error with the context it lost." {
		t.Errorf("shared rule = %+v", shared)
	}
	if len(rep.Notes) != 0 {
		t.Errorf("notes = %q, want none", rep.Notes)
	}

	_, out, _ := runCmd("rules")
	if !strings.Contains(out, "  github.com/acme/guidelines/stele.md@"+first[:7]+"  **/*.go\n  No panic in library code.\n") {
		t.Errorf("stdout = %s, want the shared rule with its commit", out)
	}
}

// On a machine that has not compiled the shared rules, stele rules lists
// the project's own and says what is missing.
func TestRulesNotesMissingSharedRules(t *testing.T) {
	sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCmd("rules", "--format", "json")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	var rep rulesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 2 {
		t.Errorf("rules = %+v, want the project's 2", rep.Rules)
	}
	note := "github.com/acme/guidelines/stele.md: its rules are not in this machine's index: run stele compile"
	if len(rep.Notes) != 1 || rep.Notes[0] != note {
		t.Errorf("notes = %q, want %q", rep.Notes, note)
	}
	if errOut != "stele rules: "+note+"\n" {
		t.Errorf("stderr = %q", errOut)
	}
}

// A pull that adds a rule to a source the index already holds, here a
// plugin's, leaves the index serving the old ones: stele rules says so.
func TestRulesNotesLockRulesTheIndexLacks(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "good")
	root := project(t, hookRules)
	withPluginSource(t, root, "good", "v1", pluginRule)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	f, err := lock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	last := f.Rules[len(f.Rules)-1]
	f.Rules = append(f.Rules, lock.Entry{ID: "0b6c3c1e-5d0e-4f43-9a4c-2f1f3e0d9a77", Source: last.Source, Hash: last.Hash, Position: last.Position + 1})
	if err := lock.Save(root, f); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCmd("rules", "--format", "json")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	var rep rulesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	note := "good: stele.lock holds rules of it that this machine's index does not: run stele compile"
	if len(rep.Notes) != 1 || rep.Notes[0] != note {
		t.Errorf("notes = %q, want %q", rep.Notes, note)
	}
	if errOut != "stele rules: "+note+"\n" {
		t.Errorf("stderr = %q", errOut)
	}
}

// A stele.lock stele cannot read, say with merge conflict markers, is
// noted: without it shared rules lose their pins. A missing one is not.
func TestRulesNotesUnreadableLock(t *testing.T) {
	root := compiled(t, rules)
	if rep := rulesJSON(t); len(rep.Notes) != 0 {
		t.Fatalf("notes = %q, want none", rep.Notes)
	}
	data, err := os.ReadFile(filepath.Join(root, "stele.lock"))
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, root, "stele.lock", "<<<<<<< HEAD\n"+string(data)+"=======\n>>>>>>> theirs\n")

	code, out, errOut := runCmd("rules", "--format", "json")
	if code != exitOK {
		t.Fatalf("rules = %d: %s", code, errOut)
	}
	var rep rulesReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 2 {
		t.Errorf("rules = %+v, want the index's 2", rep.Rules)
	}
	if len(rep.Notes) != 1 || !strings.HasPrefix(rep.Notes[0], "stele.lock: ") || !strings.Contains(rep.Notes[0], "this list may be incomplete") {
		t.Errorf("notes = %q, want one about stele.lock", rep.Notes)
	}
	if !strings.HasPrefix(errOut, "stele rules: stele.lock: ") {
		t.Errorf("stderr = %q", errOut)
	}
}

// A source's name comes from the committed stele.lock: a control character
// in it is quoted, never printed raw.
func TestRulesNotesQuoteSourceNames(t *testing.T) {
	got := absent(index.Index{}, lock.File{Rules: []lock.Entry{{ID: "a", Source: "evil\nstele rules: forged"}}})
	if len(got) != 1 || strings.Contains(got[0], "\n") {
		t.Errorf("notes = %q, want the name quoted", got)
	}
}

func TestRulesExternalSourceCarriedOver(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "witness")
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv("STELE_FAKE_WITNESS", witness)
	root := project(t, hookRules)
	withPluginSource(t, root, "witness", "v1", pluginRule)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	os.Remove(witness)

	rep := rulesJSON(t)
	var found bool
	for _, r := range rep.Rules {
		if r.Source == (ruleSource{Name: "witness"}) && r.Body == "Every exported function has a doc comment." {
			found = true
		}
	}
	if !found {
		t.Errorf("rules = %+v, want the plugin's rule", rep.Rules)
	}
	if data, err := os.ReadFile(witness); err == nil {
		t.Errorf("stele rules started a plugin: %q", data)
	}
}
