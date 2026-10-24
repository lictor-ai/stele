package main

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/check"
)

// rulesBranch makes a repository with checkRules on main, stele.json there if
// steleJSON is set, and switches to a branch named feature.
func rulesBranch(t *testing.T, steleJSON string) string {
	t.Helper()
	root := gitProject(t, checkRules)
	if steleJSON != "" {
		writeAt(t, root, "stele.json", steleJSON)
		commitAll(t, root, "stele.json")
	}
	gitT(t, root, "branch", "-M", "main")
	gitT(t, root, "checkout", "-q", "-b", "feature")
	return root
}

// A branch that changes the rules, or how strictly they are enforced, passes
// CI as it should, but says so: reviewers must not miss it (#52).
func TestCheckBaseReportsRuleFiles(t *testing.T) {
	root := rulesBranch(t, "")
	writeAt(t, root, "stele.json", `{"backstop": false}`)
	writeAt(t, root, "stele.md", checkRules+"\n---\n\nKeep functions short.\n")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	writeAt(t, root, "sub/stele.json", `{}`)
	commitAll(t, root, "weaken stele")

	code, out, errOut := runCmd("check", "--base", "main")
	if code != exitOK {
		t.Fatalf("check --base main = %d, stderr %q, stdout %q: a rules change must not fail CI", code, errOut, out)
	}
	for _, want := range []string{
		"stele.json: added on this branch: it sets how strictly the rules are enforced; review the change\n",
		"stele.md: modified on this branch: it holds the project's rules; review the change\n",
		"sub/stele.json: added on this branch: it sets how strictly the rules are enforced; review the change\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}

	_, out, _ = runCmd("check", "--base", "main", "--format", "json")
	var rep check.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	want := []check.RuleFileChange{
		{File: "stele.json", Change: "added"},
		{File: "stele.md", Change: "modified"},
		{File: "sub/stele.json", Change: "added"},
	}
	if diff := cmp.Diff(want, rep.RuleFilesChanged); diff != "" {
		t.Errorf("rule_files_changed (-want +got):\n%s", diff)
	}
}

// Deleting stele.json, or moving it away, is a change too.
func TestCheckBaseReportsDeletedRuleFiles(t *testing.T) {
	for name, remove := range map[string][]string{
		"rm": {"rm", "-q", "stele.json"},
		"mv": {"mv", "stele.json", "x.json"},
	} {
		t.Run(name, func(t *testing.T) {
			root := rulesBranch(t, `{"block_on_error": true}`)
			gitT(t, root, remove...)
			commitAll(t, root, "drop stele.json")
			_, out, errOut := runCmd("check", "--base", "main")
			if !strings.Contains(out, "stele.json: deleted on this branch") {
				t.Errorf("stdout = %q, stderr %q, want stele.json deleted", out, errOut)
			}
		})
	}
}

// stele reads STELE.md only where file names ignore case, so only there is
// it a rules change.
func TestCheckBaseRuleFileCase(t *testing.T) {
	root := rulesBranch(t, "")
	writeAt(t, root, "docs/STELE.md", "notes\n")
	commitAll(t, root, "notes")
	_, out, _ := runCmd("check", "--base", "main")
	folds := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if got := strings.Contains(out, "docs/STELE.md: added on this branch"); got != folds {
		t.Errorf("docs/STELE.md reported = %v, want %v on %s; stdout %q", got, folds, runtime.GOOS, out)
	}
}

func TestCheckReportsNoRuleFiles(t *testing.T) {
	root := rulesBranch(t, "")
	writeAt(t, root, "a.go", "package a\n")
	commitAll(t, root, "code")
	for _, args := range [][]string{{"--base", "main"}, {"--all"}, {}} {
		_, out, _ := runCmd(append([]string{"check"}, args...)...)
		if strings.Contains(out, "on this branch") {
			t.Errorf("check %v: stdout %q reports a rule file change", args, out)
		}
		_, out, _ = runCmd(append([]string{"check", "--format", "json"}, args...)...)
		if strings.Contains(out, "rule_files_changed") {
			t.Errorf("check %v --format json: %s has rule_files_changed", args, out)
		}
	}
}
