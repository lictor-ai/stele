package main

import (
	"bufio"
	"encoding/json"
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

// regradeLimits are what a regrade cannot see of a run, said once in its
// report and summary.
var regradeLimits = []string{
	"Regrade: each run's tree is set up again and its saved diff applied. Files a .gitignore kept out of the run's diff are not restored, nor are binary files (listed per run).",
	"Regrade: the saved diff is scrubbed, so where it replaced a path or the account name with <run>, <tmp>, ~ or <user>, the regraded tree differs from the agent's.",
}

// regrade grades again the runs saved in dir (a results/<out>/<time>
// folder) with today's grader, calling no agent: each run's tree is set up
// as the run's was, and its saved diff applied. Only the scenarios in
// names, when there are any. A run's time, tokens, denies and flags come
// from dir's results file (<time>.jsonl), when it has the run.
func regrade(cfg config, dir string, names []string) ([]Result, error) {
	diffs, err := filepath.Glob(filepath.Join(dir, "*.diff"))
	if err != nil {
		return nil, err
	}
	slices.Sort(diffs)
	saved := savedResults(filepath.Clean(dir) + ".jsonl")
	var out []Result
	for _, d := range diffs {
		m := diffName.FindStringSubmatch(filepath.Base(d))
		if m == nil || graders[m[1]] == nil || (len(names) > 0 && !slices.Contains(names, m[1])) {
			continue
		}
		rep, _ := strconv.Atoi(m[3])
		r, ok := saved[fmt.Sprintf("%s-%s-%d", m[1], m[2], rep)]
		if !ok {
			r = Result{Scenario: m[1], Arm: m[2], Rep: rep, Model: cfg.model}
		}
		out = append(out, regradeOne(cfg, r, d))
	}
	return out, nil
}

// savedResults reads a results file, by <scenario>-<arm>-<rep>.
func savedResults(path string) map[string]Result {
	out := map[string]Result{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r Result
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out[fmt.Sprintf("%s-%s-%d", r.Scenario, r.Arm, r.Rep)] = r
		}
	}
	return out
}

// regradeOne grades r's saved diff again, replacing r's grade. r keeps the
// error of its run, if it had one, and gains the regrade's.
func regradeOne(cfg config, r Result, diff string) Result {
	r.Grade = Grade{}
	work, err := os.MkdirTemp("", "stele-eval-regrade-")
	if err != nil {
		r.Error = addError(r.Error, err.Error())
		return r
	}
	defer os.RemoveAll(work)
	repo := filepath.Join(work, "repo")
	env := runEnv(cfg, filepath.Join(work, "config"))
	fixture := filepath.Join(cfg.scenarioDir, r.Scenario, "repo")
	if err := setup(fixture, repo, r.Arm, cfg.agent, cfg.stele, false, env); err != nil {
		r.Error = addError(r.Error, "regrade: setup: "+err.Error())
		return r
	}
	abs, _ := filepath.Abs(diff)
	// A saved diff holds no binary contents: those files can't come back.
	b, err := os.ReadFile(abs)
	if err != nil {
		r.Error = addError(r.Error, err.Error())
		return r
	}
	args := []string{"apply", "--whitespace=nowarn"}
	binaries := binaryFiles(string(b))
	for _, f := range binaries {
		args = append(args, "--exclude="+f)
	}
	if _, err := run(repo, env, "git", append(args, abs)...); err != nil {
		r.Error = addError(r.Error, "regrade: applying the saved diff: "+err.Error())
		return r
	}
	g, err := grade(cfg, r.Scenario, fixture, repo, env)
	if err != nil {
		r.Error = addError(r.Error, "regrade: grade: "+err.Error())
	}
	for _, f := range binaries {
		g.Notes = append(g.Notes, "regrade: binary file not restored, not graded: "+f)
	}
	if _, err := run(repo, env, "git", "add", "-A"); err == nil {
		if names, err := run(repo, env, "git", "-c", "core.quotepath=off", "diff", "--cached", "--name-only", "HEAD"); err == nil {
			if ung := ungraded(strings.Split(strings.TrimSpace(names), "\n")); len(ung) > 0 {
				g.Notes = append(g.Notes, "not graded: changed by the agent: "+strings.Join(ung, ", "))
			}
		}
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

// regradeReport is what the regrade used, then one line per regraded run.
func regradeReport(results []Result, steleDesc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stele: %s.\n", steleDesc)
	for _, l := range regradeLimits {
		fmt.Fprintf(&b, "%s\n", l)
	}
	b.WriteString("\n")
	for _, r := range results {
		fmt.Fprintf(&b, "%s / %s #%d: compliant=%v task_ok=%v violations=%d", r.Scenario, r.Arm, r.Rep, r.Compliant, r.TaskOK, len(r.Violations))
		for _, v := range r.Violations {
			fmt.Fprintf(&b, "\n  violation: %s", v)
		}
		for _, g := range r.Guidance {
			fmt.Fprintf(&b, "\n  guidance: %s", g)
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

// writeRegraded writes the regraded results beside dir's own:
// <time>.regraded.jsonl and <time>.regraded.md, scrubbed and checked for
// private data as a run's are. A result that still leaks has its texts
// replaced; a summary that still leaks is not written.
func writeRegraded(dir string, results []Result, model string, about []string) error {
	guard := newScrubber("")
	base := filepath.Clean(dir) + ".regraded"
	var lines []byte
	for i, r := range results {
		r.Error = string(guard.text([]byte(r.Error)))
		for j := range r.Notes {
			r.Notes[j] = string(guard.text([]byte(r.Notes[j])))
		}
		for j := range r.Violations {
			r.Violations[j] = string(guard.paths([]byte(r.Violations[j])))
		}
		line, _ := json.Marshal(r)
		if what := guard.leak(line); what != "" {
			r = withoutLeaks(guard, r, what)
			line, _ = json.Marshal(r)
		}
		results[i] = r
		lines = append(append(lines, line...), '\n')
	}
	sum := string(guard.text([]byte(summary(results, model, about))))
	if what := guard.leak([]byte(sum)); what != "" {
		return fmt.Errorf("the regraded summary still holds %s after scrubbing: not written", what)
	}
	if err := os.WriteFile(base+".jsonl", lines, 0o644); err != nil {
		return err
	}
	return os.WriteFile(base+".md", []byte(sum), 0o644)
}
