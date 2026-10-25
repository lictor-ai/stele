package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// steleFree are the arms that never run stele, so a new stele cannot change
// their results. Only they are reused.
var steleFree = []string{"none", "claudemd"}

// loadBaseline reads earlier results from a JSONL file: the runs of the
// chosen scenarios in arms not run now. Each is marked as a baseline, and
// regraded as today's grader would: a violation of a non-blocking rule,
// which graders before 2026-10-25 counted, is not a violation.
// A run is only compared with runs of the same model, with rtk or without
// as it was run.
func loadBaseline(path, scenarioDir string, scenarios, running []string, model string, rtk bool) ([]Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	defer f.Close()
	advice := map[string][]string{} // scenario -> messages of its non-blocking rules
	var out []Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r Result
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if !slices.Contains(scenarios, r.Scenario) || slices.Contains(running, r.Arm) || !slices.Contains(steleFree, r.Arm) || r.RTK != rtk || r.Model != model {
			continue
		}
		if _, ok := advice[r.Scenario]; !ok {
			advice[r.Scenario] = nonBlocking(filepath.Join(scenarioDir, r.Scenario, "repo"))
		}
		var kept []string
		for _, v := range r.Violations {
			if !endsWithAny(v, advice[r.Scenario]) {
				kept = append(kept, v)
			}
		}
		r.Violations = kept
		r.Compliant = len(kept) == 0
		r.Baseline = filepath.Base(path)
		out = append(out, r)
	}
	return out, sc.Err()
}

var adviceRule = regexp.MustCompile(`(?m)^blocking:\s*false\s*$`)

// nonBlocking returns the messages of the rules with blocking: false in the
// stele.md files under repo.
func nonBlocking(repo string) []string {
	var msgs []string
	for _, f := range ruleFiles(repo) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, rule := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(normalize(b)), -1) {
			if !adviceRule.MatchString(rule) {
				continue
			}
			for _, l := range strings.Split(rule, "\n") {
				if m, ok := strings.CutPrefix(l, "message:"); ok {
					msgs = append(msgs, strings.TrimSpace(m))
				}
			}
		}
	}
	return msgs
}

func endsWithAny(s string, suffixes []string) bool {
	for _, x := range suffixes {
		if x != "" && strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}
