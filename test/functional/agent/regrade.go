package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// diffName is a saved run's diff: <scenario>-<arm>-<rep>.diff.
var diffName = regexp.MustCompile(`^(.+)-(none|claudemd|agentsmd|stele|stele-tools)-(\d+)\.diff$`)

// regrade grades again the runs saved in dir (a results/<out>/<time>
// folder) with today's grader, calling no agent: each run's tree is set up
// as the run's was, and its saved diff applied. Only the scenarios in
// names, when there are any. It writes nothing under results/.
func regrade(cfg config, dir string, names []string) ([]Result, error) {
	diffs, err := filepath.Glob(filepath.Join(dir, "*.diff"))
	if err != nil {
		return nil, err
	}
	slices.Sort(diffs)
	var out []Result
	for _, d := range diffs {
		m := diffName.FindStringSubmatch(filepath.Base(d))
		if m == nil || graders[m[1]] == nil || (len(names) > 0 && !slices.Contains(names, m[1])) {
			continue
		}
		rep, _ := strconv.Atoi(m[3])
		out = append(out, regradeOne(cfg, m[1], m[2], rep, d))
	}
	return out, nil
}

func regradeOne(cfg config, scenario, arm string, rep int, diff string) Result {
	r := Result{Scenario: scenario, Arm: arm, Rep: rep, Model: cfg.model}
	work, err := os.MkdirTemp("", "stele-eval-regrade-")
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer os.RemoveAll(work)
	repo := filepath.Join(work, "repo")
	env := runEnv(cfg, filepath.Join(work, "config"))
	fixture := filepath.Join(cfg.scenarioDir, scenario, "repo")
	if err := setup(fixture, repo, arm, cfg.agent, cfg.stele, false, env); err != nil {
		r.Error = "setup: " + err.Error()
		return r
	}
	abs, _ := filepath.Abs(diff)
	// A saved diff holds no binary contents: those files can't come back.
	b, err := os.ReadFile(abs)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	args := []string{"apply", "--whitespace=nowarn"}
	binaries := binaryFiles(string(b))
	for _, f := range binaries {
		args = append(args, "--exclude="+f)
	}
	if _, err := run(repo, env, "git", append(args, abs)...); err != nil {
		r.Error = "applying the saved diff: " + err.Error()
		return r
	}
	g, err := grade(cfg, scenario, fixture, repo, env)
	if err != nil {
		r.Error = "grade: " + err.Error()
	}
	for _, f := range binaries {
		g.Notes = append(g.Notes, "regrade: binary file not restored, not graded: "+f)
	}
	r.Grade = g
	return r
}

var (
	binaryLine = regexp.MustCompile(`^Binary files .* differ\r?$`)
	diffHeader = regexp.MustCompile(`^diff --git a/(\S+) b/(\S+?)\r?$`)
)

// binaryFiles are the files of a diff whose changes are binary.
func binaryFiles(diff string) []string {
	var out []string
	var current string
	for _, l := range strings.Split(diff, "\n") {
		if m := diffHeader.FindStringSubmatch(l); m != nil {
			current = m[2]
			continue
		}
		if binaryLine.MatchString(l) && current != "" {
			out = append(out, current)
		}
	}
	return out
}

// regradeReport is one line per regraded run.
func regradeReport(results []Result) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s / %s #%d: compliant=%v task_ok=%v violations=%d", r.Scenario, r.Arm, r.Rep, r.Compliant, r.TaskOK, len(r.Violations))
		for _, v := range r.Violations {
			fmt.Fprintf(&b, "\n  violation: %s", v)
		}
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "\n  note: %s", n)
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "\n  error: %s", r.Error)
		}
		b.WriteString("\n")
	}
	return b.String()
}
