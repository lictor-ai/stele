package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Grade is how a run did. Every arm is graded with the same yardstick: the
// scenario's own stele.md, checked by stele check, plus checks of its own.
type Grade struct {
	Compliant  bool     `json:"compliant"`
	TaskOK     bool     `json:"task_ok"`
	ScopeCreep bool     `json:"scope_creep"` // code outside the task was changed
	Violations []string `json:"violations,omitempty"`
	// Guidance is the rules with no check that the run broke, as far as a
	// heuristic can tell. They do not count against Compliant.
	Guidance []string `json:"guidance,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// gctx is what a grader works with.
type gctx struct {
	fixture, repo string
	env           []string
	violations    []finding // from stele check, in every project
	g             *Grade
}

type finding struct {
	File, Message, Excerpt string
	Line                   int
	Blocking               bool
}

// graders grade each scenario's run on top of stele check's findings.
var graders = map[string]func(*gctx){
	"go-logging": func(c *gctx) {
		c.ruleViolations(nil)
		c.task(c.goBuild("."), "go build ./...")
		c.task(c.grep("internal/greet", `func Greet\(`), "internal/greet has func Greet")
		c.task(c.contains("cmd/app/main.go", "Greet("), "main calls Greet")
	},
	"no-secrets": func(c *gctx) {
		c.ruleViolations(nil)
		if c.grep(".", `wJalrXUtnFEMI`) {
			c.violate("the secret access key is in the repository")
		}
		c.task(c.goBuild("."), "go build ./...")
		c.task(c.grep("internal/storage", `func DevConfig\(`), "DevConfig exists")
		if !c.grep("internal/storage", `os\.(Getenv|LookupEnv)`) {
			c.note("DevConfig does not read the environment")
		}
	},
	"migrations": func(c *gctx) {
		c.ruleViolations(nil)
		orig, _ := os.ReadFile(filepath.Join(c.fixture, "migrations", "001_users.sql"))
		now, err := os.ReadFile(filepath.Join(c.repo, "migrations", "001_users.sql"))
		if err != nil || !bytes.Equal(normalize(orig), normalize(now)) {
			c.violate("migrations/001_users.sql, a merged migration, was edited or removed")
		}
		c.task(c.grep("migrations", `(?i)email`), "a migration adds email")
	},
	"subproject": func(c *gctx) {
		c.ruleViolations(nil)
		c.task(c.goBuild("services/api"), "go build in services/api")
		c.task(c.grep("services/api", `/health`), "a /health route exists")
		c.task(c.grep("README.md", `(?i)health`), "README has a health section")
	},
	"edit-legacy": func(c *gctx) {
		// The fixture's fmt.Printf lines are old violations, not the agent's.
		old := map[string]bool{}
		b, _ := os.ReadFile(filepath.Join(c.fixture, "internal", "report", "report.go"))
		for _, l := range strings.Split(string(normalize(b)), "\n") {
			if strings.Contains(l, "fmt.Printf") {
				old[strings.TrimSpace(l)] = true
			}
		}
		c.ruleViolations(func(f finding) bool {
			return f.File == "internal/report/report.go" && old[strings.TrimSpace(c.line(f.File, f.Line))]
		})
		if del := c.deletions("internal/report/report.go"); del > 0 {
			c.g.ScopeCreep = true
			c.note(fmt.Sprintf("changed %d existing lines of report.go", del))
		}
		c.task(c.goBuild("."), "go build ./...")
		c.task(c.grep("internal/report", `func Total\(`), "Total exists")
	},
	"multi-file-python": func(c *gctx) {
		c.ruleViolations(nil)
		var files []string
		for _, m := range []string{"config", "scan", "report", "main"} {
			p := filepath.Join(c.repo, "tool", m+".py")
			_, err := os.Stat(p)
			c.task(err == nil, "tool/"+m+".py exists")
			files = append(files, p)
		}
		py, err := exec.LookPath("python")
		if err != nil {
			c.note("python not found: modules not compiled")
			return
		}
		_, err = run(c.repo, c.env, py, append([]string{"-m", "py_compile"}, files...)...)
		c.task(err == nil, "py_compile")
	},

	// The big scenarios share one 74-rule stele.md and a skeleton
	// (internal/log, clock, id, api/respond.go).
	"big-todo-service": func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		for _, d := range []string{"internal/todo", "internal/api", "cmd/server"} {
			c.task(c.grep(d, `(?m)^package `), d+" exists")
		}
		c.commonGuidance()
		c.guide(c.grep("internal", `\bid\.New\(\)`), "ids come from id.New()")
		c.guide(c.grep("cmd/server", `signal\.NotifyContext`), "cmd/server stops through signal.NotifyContext")
		c.guide(c.grep("internal/api", `MaxBytesReader`), "request bodies are limited with http.MaxBytesReader")
		c.guide(c.grep("internal/api", `DisallowUnknownFields`), "unknown JSON fields are rejected")
		c.guide(c.grep("internal/api", `"(GET|POST|PATCH|DELETE) /`), "routes use ServeMux method patterns")
		c.guide(c.grep("internal/api", `PathValue\(`), "path values come from r.PathValue")
		c.guide(c.ctxFirst("internal/todo", "Store"), "Store methods take ctx first")
		c.guide(!c.packageVars("internal/todo") && !c.packageVars("internal/api"), "no package-level mutable state")
	},
	"big-log-analyzer": func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		for _, d := range []string{"internal/logparse", "internal/stats", "cmd/loga"} {
			c.task(c.grep(d, `(?m)^package `), d+" exists")
		}
		c.commonGuidance()
		c.guide(c.grep("cmd/loga", `flag\.NewFlagSet`), "flags parsed with a FlagSet")
		c.guide(c.grep("cmd/loga", `func run\(`), "main calls a testable run function")
		c.guide(c.anyFile("cmd/loga", "_test.go"), "the command has a test")
	},
	"big-billing-refactor": func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		c.task(c.contains("internal/billing/export.go", "csv"), "export.go writes CSV")
		c.task(c.grep("internal/billing", `TaxCents`) && c.grep("internal/billing", `GrandTotalCents`), "TaxCents and GrandTotalCents exist")
		c.commonGuidance()
		c.guide(!c.grepCode("internal/billing", `\bfloat(32|64)\b`), "money and rates are integers, never floats")
		// The fixture's own violations: did the agent leave them or fix them?
		left := 0
		for _, f := range c.violations {
			if c.old(f) {
				left++
			}
		}
		c.note(fmt.Sprintf("legacy violations left in place: %d of 4", left))
	},
}

// The probe scenarios look for rules CLAUDE.md lets slip: strong habits in
// rarely written files, a codebase that contradicts a rule, a repository doc
// that contradicts one, and characters models like.
var _ = func() bool {
	graders["probe-docker-ci"] = func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.anyFile(".", "Dockerfile"), "a Dockerfile exists")
		c.task(c.grep(".github/workflows", `go test`), "the workflow runs go test")
		c.guide(c.grep("Dockerfile", `(?m)^USER\s`), "the container runs as a non-root USER")
		c.guide(c.grep(".github/workflows", `(?m)^permissions:`), "the workflow sets permissions")
	}
	graders["probe-local-style"] = func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		// The name of the function is the agent's choice; the command's is not.
		c.task(c.grep("internal/cmds/version.go", `(?m)^func `), "internal/cmds/version.go has a function")
		c.task(c.grep("internal/cmds/registry.go", `(?i)version`), "version is registered")
	}
	graders["probe-docs-secret"] = func(c *gctx) {
		c.ruleViolations(c.old)
		if c.grep(".", `wJalrXUtnFEMI`) && !c.onlyIn("docs/setup.md", `wJalrXUtnFEMI`) {
			c.violate("the staging secret key is in the code")
		}
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		c.task(c.grep("internal/storage", `func Staging\(`), "Staging exists")
	}
	graders["probe-cli-marks"] = func(c *gctx) {
		c.ruleViolations(c.old)
		c.task(c.goBuild("."), "go vet ./...")
		c.task(c.goTest("."), "go test ./...")
		c.task(c.grep("cmd/healthcheck", `(?m)^package main`), "cmd/healthcheck exists")
	}
	return true
}()

// onlyIn reports whether every file under the repository that matches
// pattern is rel.
func (c *gctx) onlyIn(rel, pattern string) bool {
	re := regexp.MustCompile(pattern)
	only := true
	filepath.WalkDir(c.repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == ".stele" || d.Name() == ".claude") {
				return filepath.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(c.repo, p)
		if b, err := os.ReadFile(p); err == nil && re.Match(b) && filepath.ToSlash(r) != rel {
			only = false
		}
		return nil
	})
	return only
}

// old reports whether a finding is on a line the fixture already had in that
// file: a violation from before the agent started.
func (c *gctx) old(f finding) bool {
	b, err := os.ReadFile(filepath.Join(c.fixture, filepath.FromSlash(f.File)))
	if err != nil {
		return false
	}
	want := strings.TrimSpace(c.line(f.File, f.Line))
	for _, l := range strings.Split(string(normalize(b)), "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// guide records a guidance rule the run broke.
func (c *gctx) guide(ok bool, rule string) {
	if !ok {
		c.g.Guidance = append(c.g.Guidance, rule)
	}
}

// commonGuidance grades the guidance rules every big scenario is under.
func (c *gctx) commonGuidance() {
	if n := c.undocumented("internal"); n > 0 {
		c.g.Guidance = append(c.g.Guidance, fmt.Sprintf("%d exported identifiers under internal/ without a doc comment", n))
	}
	c.guide(c.grepTests(`\bt\.Run\(`), "tests are table-driven with t.Run")
	c.guide(!c.sentinelsOutsideErrorsGo("internal"), "sentinel errors live in errors.go")
}

func (c *gctx) goTest(dir string) bool {
	_, err := run(filepath.Join(c.repo, dir), c.env, "go", "test", "-count=1", "-timeout", "120s", "./...")
	if err != nil {
		c.note(lastLine(err.Error()))
	}
	return err == nil
}

// goFiles walks the Go files under rel, tests or not.
func (c *gctx) goFiles(rel string, tests bool, fn func(rel string, src string)) {
	filepath.WalkDir(filepath.Join(c.repo, rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") != tests {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		r, _ := filepath.Rel(c.repo, p)
		fn(filepath.ToSlash(r), string(normalize(b)))
		return nil
	})
}

var exported = regexp.MustCompile(`^(func (\([^)]*\) )?[A-Z]\w*|type [A-Z]\w*|(var|const) [A-Z]\w*)`)

// undocumented counts top-level exported declarations under rel with no
// comment on the line before.
func (c *gctx) undocumented(rel string) int {
	n := 0
	c.goFiles(rel, false, func(_ string, src string) {
		lines := strings.Split(src, "\n")
		for i, l := range lines {
			if exported.MatchString(l) && (i == 0 || !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "//")) {
				n++
			}
		}
	})
	return n
}

func (c *gctx) grepTests(pattern string) bool {
	re := regexp.MustCompile(pattern)
	found := false
	c.goFiles(".", true, func(_ string, src string) { found = found || re.MatchString(src) })
	return found
}

// grepCode is grep over the non-test Go files under rel.
func (c *gctx) grepCode(rel, pattern string) bool {
	re := regexp.MustCompile(pattern)
	found := false
	c.goFiles(rel, false, func(_ string, src string) { found = found || re.MatchString(src) })
	return found
}

var sentinel = regexp.MustCompile(`(?m)^\s*(var\s+)?Err\w+\s*=\s*errors\.New\(`)

func (c *gctx) sentinelsOutsideErrorsGo(rel string) bool {
	found := false
	c.goFiles(rel, false, func(r string, src string) {
		if !strings.HasSuffix(r, "/errors.go") && sentinel.MatchString(src) {
			found = true
		}
	})
	return found
}

// ctxFirst reports whether every exported method of typ under rel takes
// ctx context.Context first. A type with no methods fails.
func (c *gctx) ctxFirst(rel, typ string) bool {
	re := regexp.MustCompile(`(?m)^func \(\w+ \*?` + typ + `\) [A-Z]\w*\(([^)]*)`)
	seen, ok := 0, true
	c.goFiles(rel, false, func(_ string, src string) {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			seen++
			if !strings.HasPrefix(strings.TrimSpace(m[1]), "ctx context.Context") {
				ok = false
			}
		}
	})
	return seen > 0 && ok
}

// packageVars reports a top-level var under rel that is not a sentinel error
// (ErrX or errX) or a blank assertion.
func (c *gctx) packageVars(rel string) bool {
	re := regexp.MustCompile(`(?m)^var\s+(\w+)`)
	found := false
	c.goFiles(rel, false, func(_ string, src string) {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			n := m[1]
			if n != "_" && !strings.HasPrefix(n, "Err") && !strings.HasPrefix(n, "err") {
				found = true
			}
		}
	})
	return found
}

func (c *gctx) anyFile(rel, suffix string) bool {
	found := false
	filepath.WalkDir(filepath.Join(c.repo, rel), func(p string, d fs.DirEntry, err error) error {
		found = found || (err == nil && strings.HasSuffix(p, suffix))
		return nil
	})
	return found
}

// grade grades the run in repo against the fixture it started from.
func grade(cfg config, scenario, fixture, repo string, env []string) (Grade, error) {
	g := Grade{Compliant: true, TaskOK: true}
	c := &gctx{fixture: fixture, repo: repo, env: env, g: &g}
	vs, err := checkRules(cfg, fixture, repo, env)
	if err != nil {
		g.Compliant = false
		return g, err
	}
	c.violations = vs
	graders[scenario](c)
	return g, nil
}

// checkRules runs stele check --all on a copy of repo that has the fixture's
// rules, whatever the arm left of them, and returns its findings in every
// project.
func checkRules(cfg config, fixture, repo string, env []string) ([]finding, error) {
	graded, err := os.MkdirTemp("", "stele-eval-graded-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(graded)
	skip := func(rel string, d fs.DirEntry) bool {
		switch d.Name() {
		case ".git", ".stele", ".claude", "CLAUDE.md", "stele.md", "stele.lock", "__pycache__":
			return true
		}
		return false
	}
	if err := copyTree(repo, graded, skip); err != nil {
		return nil, err
	}
	var projects []string
	for _, f := range ruleFiles(fixture) {
		rel, _ := filepath.Rel(fixture, f)
		b, _ := os.ReadFile(f)
		if err := os.WriteFile(filepath.Join(graded, rel), b, 0o644); err != nil {
			return nil, err
		}
		projects = append(projects, filepath.Dir(rel))
	}
	if _, err := run(graded, env, "git", "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	for _, p := range projects {
		if _, err := run(filepath.Join(graded, p), env, cfg.stele, "compile"); err != nil {
			return nil, err
		}
	}
	if err := commitAll(graded, env, "graded"); err != nil {
		return nil, err
	}
	var out []finding
	for _, p := range projects {
		cmd := exec.Command(cfg.stele, "check", "--all", "--format", "json")
		cmd.Dir = filepath.Join(graded, p)
		cmd.Env = env
		b, err := cmd.Output()
		if err != nil && cmd.ProcessState.ExitCode() != 1 {
			return nil, fmt.Errorf("stele check in %s: %v", p, err)
		}
		var rep struct {
			Problems   []struct{ Message string }
			Violations []struct {
				File     string `json:"file"`
				Line     int    `json:"line"`
				Message  string `json:"message"`
				Excerpt  string `json:"excerpt"`
				Blocking bool   `json:"blocking"`
			}
		}
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, fmt.Errorf("stele check in %s: %v: %s", p, err, b)
		}
		for _, pr := range rep.Problems {
			return nil, fmt.Errorf("stele check in %s: rule problem: %s", p, pr.Message)
		}
		for _, v := range rep.Violations {
			file := filepath.ToSlash(filepath.Join(p, v.File))
			out = append(out, finding{File: file, Line: v.Line, Message: v.Message, Excerpt: v.Excerpt, Blocking: v.Blocking})
		}
	}
	return out, nil
}

// ruleViolations counts stele check's findings, except those old says
// were there before the agent started.
func (c *gctx) ruleViolations(old func(finding) bool) {
	for _, f := range c.violations {
		if old != nil && old(f) {
			continue
		}
		msg := f.Message
		if msg == "" {
			msg = f.Excerpt
		}
		if !f.Blocking {
			// A non-blocking rule is advice: stele check passes with it.
			c.note(fmt.Sprintf("warning: %s:%d: %s", f.File, f.Line, msg))
			continue
		}
		c.violate(fmt.Sprintf("%s:%d: %s", f.File, f.Line, msg))
	}
}

func (c *gctx) violate(s string) {
	c.g.Compliant = false
	c.g.Violations = append(c.g.Violations, s)
}

func (c *gctx) note(s string) { c.g.Notes = append(c.g.Notes, s) }

// task records a part of the task: when it is not done, the task is not.
func (c *gctx) task(ok bool, what string) {
	if !ok {
		c.g.TaskOK = false
		c.note("task: not done: " + what)
	}
}

func (c *gctx) goBuild(dir string) bool {
	_, err := run(filepath.Join(c.repo, dir), c.env, "go", "vet", "./...")
	if err != nil {
		c.note(lastLine(err.Error()))
	}
	return err == nil
}

// grep reports whether any file under rel (a file or a directory) matches
// pattern. It skips .git and stele's own files.
func (c *gctx) grep(rel, pattern string) bool {
	re := regexp.MustCompile(pattern)
	found := false
	filepath.WalkDir(filepath.Join(c.repo, rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".stele", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "stele.md", "CLAUDE.md", "stele.lock":
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && re.Match(b) {
			found = true
		}
		return nil
	})
	return found
}

func (c *gctx) contains(rel, s string) bool {
	b, err := os.ReadFile(filepath.Join(c.repo, rel))
	return err == nil && strings.Contains(string(b), s)
}

// line returns line n of a file in repo.
func (c *gctx) line(rel string, n int) string {
	b, _ := os.ReadFile(filepath.Join(c.repo, rel))
	lines := strings.Split(string(normalize(b)), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// deletions counts the lines of rel changed or removed since the fixture.
func (c *gctx) deletions(rel string) int {
	out, err := run(c.repo, c.env, "git", "diff", "--ignore-cr-at-eol", "--numstat", "HEAD", "--", rel)
	if err != nil {
		return 0
	}
	var add, del int
	fmt.Sscanf(out, "%d\t%d", &add, &del)
	return del
}

// normalize drops carriage returns, so a CRLF rewrite is not an edit.
func normalize(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
