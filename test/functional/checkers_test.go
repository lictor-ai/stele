package functional

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The checker plugins that ship with stele (ADR-0019), driven by stele
// check through the real protocol: stele finds stele-checker-opengrep and
// stele-checker-trivy on PATH, and they find a fake opengrep and trivy, which
// print output recorded from the real tools. The real-tool tests at the end
// run only where opengrep or trivy is installed.

var (
	checkersOnce sync.Once
	checkersDir  string
	checkersErr  error
)

// checkersBin builds both checker plugins and a fake scanner, once, into a
// directory of their own, and returns it. The fake scanner is in
// fake/opengrep and fake/trivy.
func checkersBin(t *testing.T) string {
	t.Helper()
	checkersOnce.Do(func() {
		dir, err := os.MkdirTemp("", "stele-checkers-")
		if err != nil {
			checkersErr = err
			return
		}
		checkersDir = dir
		for _, name := range []string{"stele-checker-opengrep", "stele-checker-trivy"} {
			out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "bin", name+exe()), "../../cmd/"+name).CombinedOutput()
			if err != nil {
				checkersErr = err
				t.Logf("building %s:\n%s", name, out)
				return
			}
		}
		src := filepath.Join(dir, "src")
		os.MkdirAll(src, 0o755)
		os.WriteFile(filepath.Join(src, "go.mod"), []byte("module fakescanner\n"), 0o644)
		os.WriteFile(filepath.Join(src, "main.go"), []byte(fakeScannerSrc), 0o644)
		fake := filepath.Join(dir, "fake", "scanner"+exe())
		cmd := exec.Command("go", "build", "-o", fake, ".")
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			checkersErr = err
			t.Logf("building the fake scanner:\n%s", out)
			return
		}
		for _, n := range []string{"opengrep", "trivy"} {
			data, _ := os.ReadFile(fake)
			if err := os.WriteFile(filepath.Join(dir, "fake", n+exe()), data, 0o755); err != nil {
				checkersErr = err
				return
			}
		}
	})
	if checkersErr != nil {
		t.Fatalf("building the checkers: %v", checkersErr)
	}
	return checkersDir
}

// fakeScannerSrc prints the file $FAKE_SCANNER_OUT with {{DIR}} made
// $FAKE_SCANNER_DIR, as JSON writes it.
const fakeScannerSrc = `package main

import (
	"bytes"
	"encoding/json"
	"os"
)

func main() {
	data, err := os.ReadFile(os.Getenv("FAKE_SCANNER_OUT"))
	if err != nil {
		os.Exit(9)
	}
	dir, _ := json.Marshal(os.Getenv("FAKE_SCANNER_DIR"))
	os.Stdout.Write(bytes.ReplaceAll(data, []byte("{{DIR}}"), dir[1:len(dir)-1]))
}
`

// withFakeScanner points p at the checker plugins and the fake scanners,
// printing the recorded output out, and at nothing else.
func (p *project) withFakeScanner(dir, out string) {
	abs, err := filepath.Abs(out)
	if err != nil {
		p.t.Fatal(err)
	}
	p.env = append(p.env,
		"PATH="+onlyPath(p.t, filepath.Join(dir, "bin"), filepath.Join(dir, "fake")),
		"FAKE_SCANNER_OUT="+abs, "FAKE_SCANNER_DIR="+p.dir)
}

// onlyPath is a PATH of dirs and the folder that holds git, which stele
// check needs: no real opengrep or trivy can be found on it.
func onlyPath(t *testing.T, dirs ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"opengrep", "trivy"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(git), tool+exe())); err == nil {
			t.Skipf("%s is next to git, in %s: no PATH can hold git without it", tool, filepath.Dir(git))
		}
	}
	return strings.Join(append(dirs, filepath.Dir(git)), string(os.PathListSeparator))
}

const opengrepMD = `scope: ["**/*.py"]
check: opengrep
rules: .opengrep/sql.yml
ids: [python-sqli]

No SQL built from strings: use parameterised queries.
`

const sqlRulesYML = `rules:
  - id: python-sqli
    languages: [python]
    severity: ERROR
    message: SQL built from a string with user input. Use a parameterised query.
    pattern: $DB.execute("..." + $X + "...")
  - id: python-eval
    languages: [python]
    severity: WARNING
    message: eval of user input
    pattern: eval(...)
`

// opengrepProject is the project cmd/stele-checker-opengrep/testdata was
// recorded from, with the rule above.
func opengrepProject(t *testing.T) *project {
	p := newProject(t, opengrepMD)
	p.write("stele.json", `{"checkers": [{"name": "opengrep"}]}`+"\n")
	p.write(".opengrep/sql.yml", sqlRulesYML)
	p.write("app/db.py", "import sqlite3\n\ndef find(db, name):\n    return db.execute(\"SELECT * FROM users WHERE name = '\" + name + \"'\")\n\neval(name)\n")
	p.write("app/label.py", "n = input()\nlabel = \"é\"; q = db.execute(\"SELECT \" + n + \"\")\n")
	p.write("app/broken.py", "def f(:\n  return db.execute(\"x\" + y + \"\")\n)))) @@@ $$$\n")
	return p
}

func TestCheckerOpengrep(t *testing.T) {
	dir := checkersBin(t)
	p := opengrepProject(t)
	p.withFakeScanner(dir, "../../cmd/stele-checker-opengrep/testdata/results.json")
	p.compile()
	p.commit()
	out := p.mustStele(1, "check", "--all")
	for _, want := range []string{
		`app/db.py:4:12: error `,
		`"No SQL built from strings: use parameterised queries.": python-sqli: SQL built from a string with user input. Use a parameterised query.`,
		`app/label.py:2:18: error `,
		"app/broken.py: skipped: opengrep: PartialParsing: Syntax error at line app/broken.py:3:",
		"1 rule valid",
		"checked 3 files: 2 errors",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stele check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "eval of user input") {
		t.Errorf("python-eval counted, though ids names only python-sqli:\n%s", out)
	}
}

// A rule naming an id the rules file doesn't have is invalid: stele check
// says so and fails, before running Opengrep.
func TestCheckerOpengrepInvalidRule(t *testing.T) {
	dir := checkersBin(t)
	p := opengrepProject(t)
	p.write("stele.md", strings.Replace(opengrepMD, "[python-sqli]", "[python-xss]", 1))
	p.withFakeScanner(dir, "../../cmd/stele-checker-opengrep/testdata/results.json")
	p.compile()
	p.commit()
	out := p.mustStele(1, "check", "--all")
	if !strings.Contains(out, `ids: "python-xss" is not the id of a rule in .opengrep/sql.yml`) {
		t.Errorf("stele check:\n%s", out)
	}
}

// No opengrep on PATH: stele check can't run, and says what to install.
func TestCheckerOpengrepMissing(t *testing.T) {
	dir := checkersBin(t)
	p := opengrepProject(t)
	p.env = append(p.env, "PATH="+onlyPath(t, filepath.Join(dir, "bin")))
	p.compile()
	p.commit()
	out := p.mustStele(2, "check", "--all")
	if !strings.Contains(out, "opengrep: not found on PATH") {
		t.Errorf("stele check:\n%s", out)
	}
}

const trivyMD = `scope: ["requirements.txt", "Dockerfile", "**/*.py"]
check: trivy
scanners: [vuln, secret]
severity: [HIGH, CRITICAL]

No dependency with a known high or critical vulnerability, and no secret in code.
`

func trivyProject(t *testing.T) *project {
	p := newProject(t, trivyMD)
	p.write("stele.json", `{"checkers": [{"name": "trivy"}]}`+"\n")
	p.write("requirements.txt", "flask==0.12\nrequests==2.31.0\n")
	p.write("Dockerfile", "FROM alpine:latest\nRUN apk add curl\n")
	p.write("config/settings.py", "import os\n\nGITHUB_TOKEN = \"not-a-real-token\"\n")
	return p
}

func TestCheckerTrivy(t *testing.T) {
	dir := checkersBin(t)
	p := trivyProject(t)
	p.withFakeScanner(dir, "../../cmd/stele-checker-trivy/testdata/report.json")
	p.compile()
	p.commit()
	out := p.mustStele(1, "check", "--all")
	for _, want := range []string{
		"requirements.txt:1:1: error ",
		": CVE-2018-1000656 (HIGH) in flask 0.12, fixed in 0.12.3: python-flask: Denial of Service via crafted JSON file",
		"config/settings.py:3:1: error ",
		": secret github-pat (CRITICAL): GitHub Personal Access Token",
		"checked 3 files: 4 errors",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stele check lacks %q:\n%s", want, out)
		}
	}
	// Only the scanners and severities the rule names.
	for _, not := range []string{"DS-0002", "CVE-2024-35195"} {
		if strings.Contains(out, not) {
			t.Errorf("stele check reports %s:\n%s", not, out)
		}
	}
}

// The real tools, where they are installed. CI doesn't have them.

func realPath(t *testing.T, dir, tool string) string {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s is not on PATH", tool)
	}
	return "PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestCheckerOpengrepReal(t *testing.T) {
	dir := checkersBin(t)
	p := opengrepProject(t)
	p.env = append(p.env, realPath(t, dir, "opengrep"))
	p.compile()
	p.commit()
	out := p.mustStele(1, "check", "--all")
	for _, want := range []string{"app/db.py:4:12: error ", "app/label.py:2:18: error ", "python-sqli: SQL built from a string"} {
		if !strings.Contains(out, want) {
			t.Errorf("stele check lacks %q:\n%s", want, out)
		}
	}
}

func TestCheckerTrivyReal(t *testing.T) {
	dir := checkersBin(t)
	p := trivyProject(t)
	p.write("stele.md", strings.Replace(trivyMD, "scanners: [vuln, secret]", "scanners: [vuln]", 1))
	p.env = append(p.env, realPath(t, dir, "trivy"))
	// Trivy keeps its database in the user's cache. The test uses the copy
	// already there and never downloads or updates one: with no copy, it
	// skips, since it must not reach the network.
	c, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no user cache directory: %v", err)
	}
	cache := filepath.Join(c, "trivy")
	if _, err := os.Stat(filepath.Join(cache, "db", "trivy.db")); err != nil {
		t.Skipf("no Trivy database in %s: run trivy once to download it", cache)
	}
	p.env = append(p.env, "TRIVY_CACHE_DIR="+cache, "TRIVY_SKIP_DB_UPDATE=true", "TRIVY_SKIP_JAVA_DB_UPDATE=true")
	p.compile()
	p.commit()
	out := p.mustStele(1, "check", "--all")
	if !strings.Contains(out, "requirements.txt:1:1: error ") || !strings.Contains(out, "CVE-2018-1000656 (HIGH) in flask 0.12") {
		t.Errorf("stele check:\n%s", out)
	}
}
