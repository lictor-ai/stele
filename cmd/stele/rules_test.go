package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/rulelist"
)

func rulesJSON(t *testing.T, args ...string) (rulelist.List, string) {
	t.Helper()
	code, out, errOut := runCmd(append([]string{"rules", "--json"}, args...)...)
	if code != exitOK {
		t.Fatalf("rules --json = %d: %s", code, errOut)
	}
	var l rulelist.List
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	return l, errOut
}

func readAt(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A root with no stele.md or stele.json has no rules: it prints none, and
// writes nothing, not even a key.
func TestRulesNoRuleFiles(t *testing.T) {
	root := project(t, "")
	code, out, errOut := runCmd("rules")
	if code != exitOK || !strings.Contains(out, "no stele.md or stele.json") {
		t.Errorf("rules = %d, %q, %q", code, out, errOut)
	}
	l, _ := rulesJSON(t)
	if l.V != rulelist.Version || len(l.Rules) != 0 || l.Missing == nil || l.Notes == nil {
		t.Errorf("JSON = %+v, want v1 and no rules", l)
	}
	if _, err := os.Stat(filepath.Join(root, ".stele")); err == nil {
		t.Error("rules made .stele/")
	}
	dir, _ := keyDir()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("rules made a key: %v", entries)
	}
}

// Before any compile, stele rules compiles as the hook would: the ids it
// prints are those stele.lock now holds, and stele.md is never written.
func TestRulesCompilesFirst(t *testing.T) {
	root := project(t, rules)
	l, _ := rulesJSON(t)
	f, err := lock.Load(root)
	if err != nil || len(f.Rules) != 2 {
		t.Fatalf("stele.lock = %+v, %v; want 2 rules", f, err)
	}
	if l.V != 1 || l.Project != f.Project || len(l.Rules) != 2 {
		t.Fatalf("list = %+v, want 2 rules of project %s", l, f.Project)
	}
	for i, r := range l.Rules {
		if r.ID != f.Rules[i].ID {
			t.Errorf("rule %d id = %s, want the lock's %s", i, r.ID, f.Rules[i].ID)
		}
		want := rulelist.Source{Kind: "stele.md", Name: "stelemd", File: "stele.md", Rule: i + 1}
		if r.Source != want || r.Scope != nil {
			t.Errorf("rule %d = %+v, want source %+v and no scope", i, r, want)
		}
	}
	if l.Rules[0].Text != "Never build SQL by string concatenation." {
		t.Errorf("text = %q", l.Rules[0].Text)
	}
	if got := readAt(t, root, "stele.md"); got != rules {
		t.Errorf("stele.md changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(root, "stele.json")); err == nil {
		t.Error("rules made stele.json")
	}

	// stele.md changed since: the new rule is listed with the id the lock
	// gives it.
	writeAt(t, root, "stele.md", rules+"\n---\n\nscope: [\"**/*.go\"]\n\nNo panic in library code.\n")
	l, _ = rulesJSON(t)
	f, _ = lock.Load(root)
	if len(l.Rules) != 3 || len(f.Rules) != 3 || l.Rules[2].ID != f.Rules[2].ID {
		t.Fatalf("after an edit: %d rules, lock %d", len(l.Rules), len(f.Rules))
	}
	if got := l.Rules[2].Scope; len(got) != 1 || got[0] != "**/*.go" {
		t.Errorf("scope = %v", got)
	}
}

// --json is --format json, and the JSON has a scope of null for every file.
func TestRulesFormats(t *testing.T) {
	compiled(t, rules)
	_, viaJSON, _ := runCmd("rules", "--json")
	_, viaFormat, _ := runCmd("rules", "--format", "json")
	if viaJSON != viaFormat || !strings.Contains(viaJSON, `"scope": null`) {
		t.Errorf("--json:\n%s\n--format json:\n%s", viaJSON, viaFormat)
	}
	if _, also, _ := runCmd("rules", "--json", "--format", "json"); also != viaJSON {
		t.Errorf("--json --format json:\n%s", also)
	}
}

// The text is one line per rule: id, source and place, scope, excerpt; then
// a count.
func TestRulesText(t *testing.T) {
	root := compiled(t, rules+"\n---\n\nscope: [\"**/*.go\", \"!vendor/**\"]\n\nNo panic in library code.\nNot even in init.\n")
	f, _ := lock.Load(root)
	code, out, errOut := runCmd("rules")
	if code != exitOK || errOut != "" {
		t.Fatalf("rules = %d, stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("text:\n%s\nwant 3 rules and a count", out)
	}
	for i, want := range [][]string{
		{f.Rules[0].ID, "stele.md rule 1", "every file", `"Never build SQL by string concatenation."`},
		{f.Rules[1].ID, "stele.md rule 2", "every file", `"Every handler goes through the middleware chain."`},
		{f.Rules[2].ID, "stele.md rule 3", "**/*.go, !vendor/**", `"No panic in library code. Not even in init."`},
	} {
		if !strings.HasPrefix(lines[i], want[0]) {
			t.Errorf("line %d = %q, want it to start with %s", i, lines[i], want[0])
		}
		for _, w := range want[1:] {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d = %q, want %q", i, lines[i], w)
			}
		}
	}
	if lines[3] != "3 rules" {
		t.Errorf("count = %q", lines[3])
	}
}

// A stele.md that stops parsing: the last valid rules, as the hook serves
// them, with a note; with no earlier rules, stele rules cannot run.
func TestRulesBrokenSteleMD(t *testing.T) {
	root := compiled(t, rules)
	writeAt(t, root, "stele.md", "tags: [a]\n")
	l, errOut := rulesJSON(t)
	if len(l.Rules) != 2 || len(l.Notes) != 1 || !strings.Contains(l.Notes[0], "stele.md:1") {
		t.Errorf("list = %+v, want the last 2 rules and a note naming stele.md:1", l)
	}
	if errOut != "" {
		t.Errorf("--json stderr = %q, want the note in the JSON only", errOut)
	}
	code, out, errOut := runCmd("rules")
	if code != exitOK || !strings.Contains(out, "2 rules") || !strings.Contains(errOut, "stele rules: stele.md:1") {
		t.Errorf("rules = %d, stdout %q, stderr %q; want the rules and the note on stderr", code, out, errOut)
	}
	if got := readAt(t, root, "stele.md"); got != "tags: [a]\n" {
		t.Errorf("stele.md changed: %q", got)
	}

	project(t, "tags: [a]\n")
	code, _, errOut = runCmd("rules", "--json")
	if code != exitCantRun || !strings.HasPrefix(errOut, "stele rules: ") {
		t.Errorf("rules on a broken stele.md with no index = %d, %q; want 2", code, errOut)
	}
}

// failWriter is a stdout that is gone, such as a closed pipe.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// A stdout that fails is a run that did not print: exit 2, in either
// format, with rules or without.
func TestRulesStdoutFails(t *testing.T) {
	for _, steleMD := range []string{rules, ""} {
		project(t, steleMD)
		for _, args := range [][]string{{"rules"}, {"rules", "--json"}} {
			var errOut strings.Builder
			if code := run(args, failWriter{}, &errOut); code != exitCantRun || !strings.HasPrefix(errOut.String(), "stele rules: ") {
				t.Errorf("%v (stele.md %q) with stdout gone = %d, %q; want 2", args, steleMD, code, errOut.String())
			}
		}
	}
}

func TestRulesBadArgs(t *testing.T) {
	compiled(t, rules)
	for _, args := range [][]string{{"--format", "xml"}, {"--json", "--format", "text"}, {"extra"}, {"--nope"}} {
		code, out, errOut := runCmd(append([]string{"rules"}, args...)...)
		if code != exitCantRun || out != "" || !strings.HasPrefix(errOut, "stele rules: ") && !strings.Contains(errOut, "flag provided but not defined") {
			t.Errorf("rules %v = %d, %q, %q; want 2", args, code, out, errOut)
		}
	}
}

func TestRulesNoKey(t *testing.T) {
	project(t, rules)
	keyDir = func() (string, error) { return "", os.ErrPermission }
	code, _, errOut := runCmd("rules")
	if code != exitCantRun || !strings.HasPrefix(errOut, "stele rules: ") {
		t.Errorf("rules with no key = %d, %q; want 2", code, errOut)
	}
}

// A git source's rules name the repository, ref, file and commit. On a
// fresh clone they are not in the index: stele rules lists the source as
// missing, and never runs git to read it.
func TestRulesGitSource(t *testing.T) {
	_, _, first := sharedRemote(t)
	root := project(t, rules)
	withGitSource(t, root, "main")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d: %s", code, errOut)
	}
	l, _ := rulesJSON(t)
	if len(l.Rules) != 4 || len(l.Missing) != 0 {
		t.Fatalf("list = %+v, want 4 rules, none missing", l)
	}
	name := "git:github.com/acme/guidelines/stele.md"
	want := rulelist.Source{Kind: "git", Name: name, Repo: sharedRepo, Ref: "main", File: "stele.md", Commit: first, Rule: 2}
	if got := l.Rules[3].Source; got != want {
		t.Errorf("source = %+v, want %+v", got, want)
	}
	if got := l.Rules[3].Scope; len(got) != 1 || got[0] != "**/*.go" {
		t.Errorf("scope = %v", got)
	}
	jsonBefore := readAt(t, root, "stele.json")

	// A fresh clone, with the remote gone: git would fail if it ran.
	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}
	localRemotes[sharedRepo] = filepath.Join(t.TempDir(), "gone.git")
	cache, _ := gitCacheDir()
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	l, _ = rulesJSON(t)
	if len(l.Rules) != 2 || l.Rules[0].Source.Kind != "stele.md" {
		t.Errorf("rules = %+v, want stele.md's 2", l.Rules)
	}
	wantMissing := rulelist.Source{Kind: "git", Name: name, Repo: sharedRepo, Ref: "main", File: "stele.md", Commit: first}
	if len(l.Missing) != 1 || l.Missing[0] != wantMissing {
		t.Errorf("missing = %+v, want %+v", l.Missing, wantMissing)
	}
	code, _, errOut := runCmd("rules")
	if code != exitOK || !strings.Contains(errOut, "github.com/acme/guidelines/stele.md") || !strings.Contains(errOut, "run stele compile") {
		t.Errorf("rules = %d, stderr %q; want the missing source named", code, errOut)
	}
	if got := readAt(t, root, "stele.json"); got != jsonBefore {
		t.Errorf("stele.json changed:\n%s", got)
	}
}

// stele rules never starts a plugin (D79): an external source's rules come
// from the index, and on a fresh clone the source is missing.
func TestRulesNeverRunsPlugins(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "witness")
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv("STELE_FAKE_WITNESS", witness)
	root := project(t, hookRules)
	withPluginSource(t, root, "witness", "v1", pluginRule)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	if err := os.Remove(witness); err != nil {
		t.Fatalf("the plugin did not run at compile: %v", err)
	}
	l, _ := rulesJSON(t)
	last := l.Rules[len(l.Rules)-1]
	if last.Source != (rulelist.Source{Kind: "plugin", Name: "witness", Rule: 1}) || last.Text != "Every exported function has a doc comment." {
		t.Errorf("plugin rule = %+v", last)
	}

	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}
	l, _ = rulesJSON(t)
	if len(l.Missing) != 1 || l.Missing[0] != (rulelist.Source{Kind: "plugin", Name: "witness"}) {
		t.Errorf("missing = %+v, want the plugin source", l.Missing)
	}
	if data, err := os.ReadFile(witness); err == nil {
		t.Errorf("a plugin ran from stele rules: %q", data)
	}
}
