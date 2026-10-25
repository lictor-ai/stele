package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/lock"
)

const rules = "tags: [db]\n\nNever build SQL by string concatenation.\n\n---\n\nEvery handler goes through the middleware chain.\n"

// project makes a temp repository holding stele.md and enters it.
func project(t *testing.T, steleMD string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if steleMD != "" {
		if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(steleMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	// Never touch the real user's key.
	dir := t.TempDir()
	old := keyDir
	keyDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { keyDir = old })
	return root
}

func runCmd(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCompile(t *testing.T) {
	root := project(t, rules)

	code, out, errOut := runCmd("compile")
	if code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"2 rules: 0 matched by text, 0 by position, 2 new, 0 dropped", "wrote stele.lock, .stele/index.json and .claude/rules/stele-rules.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	for _, f := range []string{"stele.lock", ".stele/index.json", ".stele/.gitignore", ".claude/rules/stele-rules.md"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// Run again: nothing to do.
	code, out, _ = runCmd("compile")
	if code != exitOK || !strings.Contains(out, "2 matched by text") || !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("second compile = %d, stdout %q, want up to date", code, out)
	}

	// Reword the second rule: its id is kept by position, and named.
	reworded := strings.Replace(rules, "Every handler goes through", "Every HTTP handler uses", 1)
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(reworded), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCmd("compile")
	if code != exitOK || !strings.Contains(out, "1 by position") || !strings.Contains(out, "stelemd rule 2") {
		t.Errorf("compile after reword = %d, stdout %q, want the guess named", code, out)
	}
}

// An invalid rule is written to stele.lock and the index like any other, so
// it keeps its id and the valid rules are enforced, but compile exits 1 (#54).
func TestCompileInvalidRule(t *testing.T) {
	const valid = "check: regex\npattern: password\\s*=\n\nNever put credentials in code.\n"
	root := project(t, valid+"\n---\n\ncheck: regex\npattern: x*\n\nNo x.\n")

	code, out, errOut := runCmd("compile")
	if code != exitFailed {
		t.Fatalf("compile = %d, stderr %q, want %d for an invalid rule", code, errOut, exitFailed)
	}
	for _, want := range []string{"invalid rule ", `"No x."`, "matches empty text", "(stelemd rule 2)", "1 problem in the rules", "wrote stele.lock, .stele/index.json and .claude/rules/stele-rules.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	lf, err := lock.Load(root)
	if err != nil || len(lf.Rules) != 2 {
		t.Fatalf("stele.lock: %d rules, %v; want both rules", len(lf.Rules), err)
	}
	id := lf.Rules[1].ID

	// Run again: nothing written, still invalid.
	code, out, _ = runCmd("compile")
	if code != exitFailed || !strings.Contains(out, "up to date, nothing written") || !strings.Contains(out, "matches empty text") {
		t.Errorf("second compile = %d, stdout %q, want up to date and the problem", code, out)
	}

	// stele check names the rule, not an out-of-date lock.
	writeAt(t, root, "a.go", "package a\n")
	code, out, errOut = runCmd("check", "a.go")
	if code != exitFailed || !strings.Contains(out, "invalid rule "+id) || strings.Contains(out+errOut, "out of date") {
		t.Errorf("check = %d, stdout %q, stderr %q, want the invalid rule named", code, out, errOut)
	}

	// Fix the pattern: exit 0, and the rule keeps its id.
	writeAt(t, root, "stele.md", valid+"\n---\n\ncheck: regex\npattern: x+\n\nNo x.\n")
	if code, out, _ := runCmd("compile"); code != exitOK {
		t.Fatalf("compile after the fix = %d, stdout %q", code, out)
	}
	if lf, _ := lock.Load(root); len(lf.Rules) != 2 || lf.Rules[1].ID != id {
		t.Errorf("the fixed rule's id changed: %+v, want %s", lf.Rules, id)
	}
}

func TestCompileInvalidRuleKinds(t *testing.T) {
	for name, meta := range map[string]string{
		"unknown checker": "check: eslint\n",
		"bad blocking":    "check: regex\npattern: x+\nblocking: maybe\n",
	} {
		t.Run(name, func(t *testing.T) {
			project(t, meta+"\nA rule.\n")
			if code, out, _ := runCmd("compile"); code != exitFailed || !strings.Contains(out, "invalid rule ") {
				t.Errorf("compile = %d, stdout %q, want exit %d and the problem", code, out, exitFailed)
			}
		})
	}
}

func TestCompileFindsRootFromSubdir(t *testing.T) {
	root := project(t, rules)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "stele.lock")); err != nil {
		t.Errorf("stele.lock not at the root: %v", err)
	}
}

func TestCompileRejectsArguments(t *testing.T) {
	project(t, rules)
	code, _, errOut := runCmd("compile", "extra")
	if code != exitCantRun || !strings.Contains(errOut, `unexpected argument "extra"`) {
		t.Errorf("compile extra = %d, stderr %q", code, errOut)
	}
}

// snapshot lists every file under root with its content, leaving out .git:
// git refreshes its own index when stele asks it what changed.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel + "=" + string(data) + ";")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
