package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// buildTestStele builds stele from this module into a temp directory.
func buildTestStele(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds stele")
	}
	bin := filepath.Join(t.TempDir(), "stele")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stele")
	cmd.Dir = filepath.Join("..", "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building stele: %v\n%s", err, out)
	}
	return bin
}

// A subproject's files are graded against its own rules and against the
// root's, and nothing is left "in subproject".
func TestCheckRulesGradesSubprojects(t *testing.T) {
	cfg := config{stele: buildTestStele(t)}
	env := runEnv(cfg, t.TempDir())
	fixture, repo := t.TempDir(), t.TempDir()
	rules := map[string]string{
		"stele.md":              "check: regex\npattern: \\bTODO\\b\nmessage: no TODO\n\nNever leave TODO notes.\n",
		"services/api/stele.md": "scope: [\"**/*.go\"]\ncheck: regex\npattern: context\\.Background\\(\\)\nmessage: use r.Context()\n\nUse the request's context.\n",
	}
	writeFiles(t, fixture, rules)
	writeFiles(t, repo, map[string]string{
		"README.md":                "# x\n\nTODO: readiness\n",
		"services/api/main.go":     "package main\n\n// TODO later\nfunc main() {}\n",
		"services/api/health.go":   "package main\n\nimport \"context\"\n\nvar _ = context.Background()\n",
		"services/api/stele.md":    "changed by the agent\n", // left out: the fixture's rules are put back
		"services/api/CLAUDE.md":   "TODO in a rules file\n", // left out of the graded copy
		"services/api/db/db.go":    "package db\n",
		"services/api/.gitignore":  "out.txt\n",
		"services/api/out.txt":     "TODO in an ignored file\n",
		"services/api/go.mod":      "module api\n\ngo 1.22\n",
		"services/api/testdata.md": "fine\n",
	})
	got, err := checkRules(cfg, fixture, repo, env)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range got.findings {
		found = append(found, f.File+": "+f.Message)
	}
	slices.Sort(found)
	want := []string{
		"README.md: no TODO",
		"services/api/health.go: use r.Context()",
		"services/api/main.go: no TODO",
	}
	if !slices.Equal(found, want) {
		t.Errorf("findings = %q, want %q", found, want)
	}
	for _, s := range got.skipped {
		if strings.Contains(s, "in subproject") {
			t.Errorf("skipped %q: a subproject's file went ungraded", s)
		}
	}
	if !slices.Contains(got.skipped, "services/api/out.txt: ignored by .gitignore") {
		t.Errorf("skipped = %q, want the ignored file listed", got.skipped)
	}
}

func TestBinaryFiles(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n" +
		"diff --git a/t/__pycache__/m.pyc b/t/__pycache__/m.pyc\nnew file mode 100644\nindex 0000000..457a75f\nBinary files /dev/null and b/t/__pycache__/m.pyc differ\r\n"
	if got := binaryFiles(diff); !slices.Equal(got, []string{"t/__pycache__/m.pyc"}) {
		t.Errorf("binaryFiles = %q", got)
	}
}

func TestRegradeReport(t *testing.T) {
	got := regradeReport([]Result{{Scenario: "s", Arm: "stele", Rep: 1, Grade: Grade{Guidance: []string{"tests are table-driven"}, Notes: []string{"n"}}}}, "v9.9.9")
	for _, want := range []string{"stele: v9.9.9.", "not restored", "  guidance: tests are table-driven", "  note: n"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestSavedResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	writeFiles(t, filepath.Dir(path), map[string]string{"x.jsonl": `{"scenario":"s","arm":"stele","rep":2,"input_tokens":7}` + "\nnot json\n"})
	got := savedResults(path)
	if r, ok := got["s-stele-2"]; !ok || r.InputTokens != 7 || len(got) != 1 {
		t.Errorf("savedResults = %+v", got)
	}
}
