package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// stats are the means of one scenario and arm over its runs.
type stats struct {
	n                                 int
	secs, apiSecs, turns              float64
	inTok, outTok, cacheRead, cacheCr float64
	cost                              float64
	compliant, taskOK, creep          float64
	denies, blocks, hookBytes, bash   float64
	violations, guidance              float64
	errors                            int
}

// tokens is all input and output tokens, cached or not.
func (s stats) tokens() float64 { return s.inTok + s.cacheCr + s.cacheRead + s.outTok }

func mean(rs []Result) stats {
	var s stats
	for _, r := range rs {
		s.n++
		s.secs += float64(r.DurationMS) / 1000
		s.apiSecs += float64(r.DurationAPIMS) / 1000
		s.turns += float64(r.NumTurns)
		s.inTok += float64(r.InputTokens)
		s.cacheCr += float64(r.CacheCreate)
		s.cacheRead += float64(r.CacheRead)
		s.outTok += float64(r.OutputTokens)
		s.cost += r.CostUSD
		s.compliant += b2f(r.Compliant)
		s.taskOK += b2f(r.TaskOK)
		s.creep += b2f(r.ScopeCreep)
		s.denies += float64(r.Denies)
		s.blocks += float64(r.Blocks)
		s.hookBytes += float64(r.HookBytes)
		s.bash += float64(r.BashWrites)
		s.violations += float64(len(r.Violations))
		s.guidance += float64(len(r.Guidance))
		if r.Error != "" {
			s.errors++
		}
	}
	if s.n == 0 {
		return s
	}
	d := float64(s.n)
	for _, p := range []*float64{&s.secs, &s.apiSecs, &s.turns, &s.inTok, &s.cacheCr, &s.cacheRead, &s.outTok, &s.cost,
		&s.compliant, &s.taskOK, &s.creep, &s.denies, &s.blocks, &s.hookBytes, &s.bash, &s.violations, &s.guidance} {
		*p /= d
	}
	return s
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// summary renders the results as Markdown: one table of means per scenario
// and arm, then stele's deltas against the other arms.
func summary(results []Result, model string) string {
	by := map[string]map[string][]Result{}
	var scenarios []string
	total, ran := 0.0, 0
	reused := map[string]map[string]bool{} // baseline file -> arms
	for _, r := range results {
		if by[r.Scenario] == nil {
			by[r.Scenario] = map[string][]Result{}
			scenarios = append(scenarios, r.Scenario)
		}
		by[r.Scenario][r.Arm] = append(by[r.Scenario][r.Arm], r)
		if r.Baseline != "" {
			if reused[r.Baseline] == nil {
				reused[r.Baseline] = map[string]bool{}
			}
			reused[r.Baseline][r.Arm] = true
			continue
		}
		total += r.CostUSD
		ran++
	}
	slices.Sort(scenarios)

	var b strings.Builder
	fmt.Fprintf(&b, "# Agent A/B eval\n\nModel %s, %d runs now ($%.2f at list price), %d reused. Means per run. Tokens are input + cache writes + cache reads + output.\n\n",
		model, ran, total, len(results)-ran)
	for _, f := range slices.Sorted(maps.Keys(reused)) {
		fmt.Fprintf(&b, "Reused from %s: %s, regraded with blocking violations only.\n", f, strings.Join(slices.Sorted(maps.Keys(reused[f])), ", "))
	}
	if len(reused) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("| scenario | arm | n | time s | turns | tokens | output tok | cost $ | compliant | violations | guidance missed | task ok | scope creep | denies | blocks | hook bytes | bash writes | errors |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	all := map[string][]Result{}
	for _, sc := range scenarios {
		for _, a := range arms {
			rs := by[sc][a]
			if len(rs) == 0 {
				continue
			}
			all[a] = append(all[a], rs...)
			row(&b, sc, a, mean(rs))
		}
	}
	for _, a := range arms {
		if len(all[a]) > 0 {
			row(&b, "**all**", a, mean(all[a]))
		}
	}

	b.WriteString("\n## stele against the other arms\n\nDifference of means, stele minus the other arm (time and tokens also as a percentage).\n\n")
	b.WriteString("| scenario | arm | vs | time s | tokens | cost $ | compliant | violations | guidance missed | task ok |\n|---|---|---|---|---|---|---|---|---|---|\n")
	delta := func(name string, rs map[string][]Result) {
		for _, arm := range []string{"stele", "stele-tools"} {
			if len(rs[arm]) == 0 {
				continue
			}
			s := mean(rs[arm])
			for _, a := range []string{"none", "claudemd", "stele"} {
				if a == arm || len(rs[a]) == 0 {
					continue
				}
				o := mean(rs[a])
				fmt.Fprintf(&b, "| %s | %s | %s | %+.1f (%s) | %+.0f (%s) | %+.3f (%s) | %+.0f pt | %+.1f | %+.1f | %+.0f pt |\n", name, arm, a,
					s.secs-o.secs, pct(s.secs, o.secs), s.tokens()-o.tokens(), pct(s.tokens(), o.tokens()),
					s.cost-o.cost, pct(s.cost, o.cost), 100*(s.compliant-o.compliant), s.violations-o.violations,
					s.guidance-o.guidance, 100*(s.taskOK-o.taskOK))
			}
		}
	}
	for _, sc := range scenarios {
		delta(sc, by[sc])
	}
	delta("**all**", all)

	b.WriteString("\n## Violations and notes\n\n")
	for _, r := range results {
		if len(r.Violations)+len(r.Notes)+len(r.Guidance) == 0 && r.Error == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s / %s #%d:", r.Scenario, r.Arm, r.Rep)
		for _, v := range r.Violations {
			fmt.Fprintf(&b, " **%s**;", v)
		}
		for _, g := range r.Guidance {
			fmt.Fprintf(&b, " guidance: %s;", g)
		}
		for _, n := range r.Notes {
			fmt.Fprintf(&b, " %s;", n)
		}
		if r.Error != "" {
			fmt.Fprintf(&b, " error: %s", r.Error)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func row(b *strings.Builder, sc, a string, s stats) {
	fmt.Fprintf(b, "| %s | %s | %d | %.1f | %.1f | %.0f | %.0f | %.3f | %.0f%% | %.1f | %.1f | %.0f%% | %.0f%% | %.1f | %.1f | %.0f | %.1f | %d |\n",
		sc, a, s.n, s.secs, s.turns, s.tokens(), s.outTok, s.cost, 100*s.compliant, s.violations, s.guidance, 100*s.taskOK, 100*s.creep,
		s.denies, s.blocks, s.hookBytes, s.bash, s.errors)
}

func pct(a, b float64) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.0f%%", 100*(a-b)/b)
}
