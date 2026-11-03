package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/stats"
)

// statsLoc is the zone stats counts days in: the user's. Tests set it.
var statsLoc = time.Local

// maxBroken is how many broken rules the text shows; the JSON has them all.
const maxBroken = 10

// runStats says how the rules are doing, from the log alone (D70). It reads
// and writes nothing else, runs no plugin but the built-in sources, and
// never uses the network. It exits 0, or 2 when it could not run: it is not
// a gate.
func runStats(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stele stats", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sinceArg := flags.String("since", "30d", "the window: `30d`, 12h, 2w, a day such as 2026-10-01, or all")
	format := flags.String("format", "text", "output `format`: text or json")
	if err := flags.Parse(args); err != nil {
		return exitCantRun
	}
	cantRun := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stele stats: "+format+"\n", a...)
		return exitCantRun
	}
	if *format != "text" && *format != "json" {
		return cantRun("--format is text or json, not %q", *format)
	}
	if flags.NArg() > 0 {
		return cantRun("takes no arguments, got %q", flags.Arg(0))
	}
	until := now()
	since, err := stats.ParseSince(*sinceArg, until, statsLoc)
	if err != nil {
		return cantRun("--since %s, not %q", errText(err), *sinceArg)
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}
	in := stats.Input{Since: since, Until: until, Loc: statsLoc}
	if noRuleFiles(root) {
		if *format == "json" {
			printStatsJSON(stdout, stats.Compute(in, nil, stats.LogCounts{}))
		} else {
			fmt.Fprintf(stdout, "no stele.md or stele.json in %s: no rules to report on\n", showPath(root))
		}
		return exitOK
	}

	committed, _ := lock.Load(root)
	in.Project = committed.Project
	key, kerr := loadExistingKey()
	in.Rules, in.RulesKnown = statsRules(root, committed, key, stderr)

	var lines []stats.Line
	var counts stats.LogCounts
	if kerr == nil {
		if lines, counts, err = stats.Collect(root, key, in); err != nil {
			return cantRun("%v", err)
		}
	} else if !errors.Is(kerr, fs.ErrNotExist) {
		return cantRun("%v", kerr)
	}
	rep := stats.Compute(in, lines, counts)
	if *format == "json" {
		printStatsJSON(stdout, rep)
	} else {
		printStats(stdout, rep, *sinceArg)
	}
	return exitOK
}

// errText is a --since error as the message's middle.
func errText(err error) string {
	if errors.Is(err, stats.ErrSince) {
		return err.Error()
	}
	return "must not be in the future"
}

// statsRules are the current rules, read through the sources as stele check
// reads them, writing nothing. A rule stele.lock does not have yet is new:
// its id is not settled. When stele.md does not compile, the last good
// index stands in, as it does in the hook; with neither, no rule is judged.
func statsRules(root string, committed lock.File, key []byte, stderr io.Writer) ([]stats.Rule, bool) {
	inLock := map[string]bool{}
	for _, e := range committed.Rules {
		inLock[e.ID] = true
	}
	r, err := compile.Plan(context.Background(), root, sources(), generated(), uuid.NewString)
	idx := r.Index
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "stele stats: %v\n", err)
		last, lerr := index.Load(root, key)
		if lerr != nil {
			fmt.Fprintln(stderr, "stele stats: no earlier valid rules either: dead rules are not judged")
			return nil, false
		}
		fmt.Fprintln(stderr, "stele stats: judging the last valid rules")
		idx = last
	case r.LockChanged:
		fmt.Fprintf(stderr, "stele stats: %s is out of date with the rules: run stele compile\n", lock.FileName)
	}
	rules := make([]stats.Rule, len(idx.Rules))
	for i, e := range idx.Rules {
		rules[i] = stats.Rule{ID: e.ID, Excerpt: check.Excerpt(e.Body), Scope: e.Scope, New: !inLock[e.ID]}
	}
	return rules, true
}

func printStatsJSON(w io.Writer, rep stats.Report) {
	data, _ := json.MarshalIndent(rep, "", "  ")
	w.Write(append(data, '\n'))
}

// window names the window as --since gave it.
func window(arg string) string {
	unit := map[byte]string{'h': "hour", 'd': "day", 'w': "week"}
	switch {
	case arg == "all":
		return "since the first line"
	case len(arg) > 1 && unit[arg[len(arg)-1]] != "":
		n := arg[:len(arg)-1]
		if n == "1" {
			return "last " + unit[arg[len(arg)-1]]
		}
		return "last " + n + " " + unit[arg[len(arg)-1]] + "s"
	}
	return "since " + arg
}

// printStats prints the report for a person. Below stats.MinTurns it says
// so first, and judges no rule.
func printStats(w io.Writer, rep stats.Report, sinceArg string) {
	loc := statsLoc
	day := func(t time.Time) string { return t.In(loc).Format("2006-01-02") }
	win := window(sinceArg)
	if rep.Log.Lines == 0 {
		fmt.Fprintf(w, "no log yet: stele records what it does once its hooks run here (stele init)\n")
		return
	}
	if rep.Log.Used == 0 {
		in := win
		if strings.HasPrefix(win, "last ") {
			in = "in the " + win
		}
		fmt.Fprintf(w, "nothing logged %s (%s before)\n", in, plural(rep.Log.Before, "line"))
		printSkipped(w, rep.Log)
		return
	}
	from := "the first line"
	if rep.Since != nil {
		from = day(*rep.Since)
	}
	fmt.Fprintf(w, "%s, %s to %s (%s), from .stele/log.jsonl\n", win, from, day(rep.Until), rep.TZ)
	a := rep.All
	fmt.Fprintf(w, "%s, %s with a write, %s (%d checked), %d denied%s\n",
		plural(a.Sessions, "session"), plural(a.Turns, "turn"), plural(a.Writes, "write"), a.WritesChecked,
		a.Denied.Total, deniedWhat(a.Denied.Write, a.Denied.Shell))
	if !a.Enough {
		fmt.Fprintf(w, "\nToo little data to judge the rules: %s with a write logged, stats need %d. The counts below are what happened, not a trend.\n",
			plural(a.Turns, "turn"), rep.Need.Turns)
	}

	fmt.Fprintln(w)
	printBroken(w, a.Broken)
	fmt.Fprintln(w)
	printDead(w, rep, a)
	fmt.Fprintln(w)
	printDays(w, a.DeniedByDay)

	if rep.CheckRuns.Runs > 0 {
		fmt.Fprintf(w, "\nstele check: %s, %d found violations\n", plural(rep.CheckRuns.Runs, "run"), rep.CheckRuns.WithViolations)
	}
	if len(rep.Agents) > 1 {
		fmt.Fprintln(w, "\nBy agent")
		for _, ag := range rep.Agents {
			fmt.Fprintf(w, "  %s: %s, %s", agentLabel(ag.Agent), plural(ag.Sessions, "session"), plural(ag.Turns, "turn"))
			if !ag.Enough {
				fmt.Fprintln(w, ": too little data")
				continue
			}
			writes, fixed := 0, 0
			for _, b := range ag.Broken {
				writes += b.Writes
				fixed += b.Fixed
			}
			fmt.Fprintf(w, ", %d denied, %d broken", ag.Denied.Total, writes)
			if writes > 0 {
				fmt.Fprintf(w, " (%d fixed)", fixed)
			}
			fmt.Fprintf(w, ", %d dead\n", len(ag.Dead))
		}
	}
	printSkipped(w, rep.Log)
}

func printBroken(w io.Writer, bs []stats.Broken) {
	if len(bs) == 0 {
		fmt.Fprintln(w, "Broken after a write: none")
		return
	}
	fmt.Fprintln(w, "Broken most often (fixed: the agent's next write to the file, in the same turn, did not break it)")
	for i, b := range bs {
		if i == maxBroken {
			fmt.Fprintf(w, "  ... %d more in --format json\n", len(bs)-maxBroken)
			break
		}
		var what []string
		if b.Writes > 0 {
			what = append(what, fmt.Sprintf("%s, fixed %d of %d", plural(b.Writes, "write"), b.Fixed, b.Writes))
		}
		if b.InCheck > 0 {
			what = append(what, fmt.Sprintf("%d in stele check", b.InCheck))
		}
		gone := ""
		if !b.InRules {
			gone = " (no longer in the rules)"
		}
		fmt.Fprintf(w, "  %s  %s \"%s\"%s\n", strings.Join(what, ", "), b.RuleID, b.Excerpt, gone)
	}
}

func printDead(w io.Writer, rep stats.Report, a stats.Numbers) {
	switch {
	case !rep.RulesJudged:
		fmt.Fprintln(w, "Dead: not judged, the rules do not compile")
		return
	case !a.Enough:
		fmt.Fprintf(w, "Dead: not judged until %d turns are logged (%d so far)\n", rep.Need.Turns, a.Turns)
		return
	case len(a.Dead) == 0:
		fmt.Fprintln(w, "Dead: none")
	default:
		fmt.Fprintf(w, "Dead: no write met a file they apply to, in %s or more\n", plural(rep.Need.Turns, "turn"))
		for _, d := range a.Dead {
			fmt.Fprintf(w, "  %s \"%s\" scope: %s\n", d.RuleID, d.Excerpt, scopeText(d.Scope))
		}
	}
	if n := len(a.NotJudged); n > 0 {
		fmt.Fprintf(w, "%s not judged yet: fewer than %d turns since a session first loaded %s\n", plural(n, "rule"), rep.Need.Turns, them(n))
	}
	if n := len(a.New); n > 0 {
		fmt.Fprintf(w, "%s new: no session has loaded %s yet\n", plural(n, "rule"), them(n))
	}
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func scopeText(s []string) string {
	if len(s) == 0 {
		return "every file"
	}
	return strings.Join(s, ", ")
}

func printDays(w io.Writer, days []stats.Day) {
	var denied []stats.Day
	for _, d := range days {
		if d.Total > 0 {
			denied = append(denied, d)
		}
	}
	if len(denied) == 0 {
		fmt.Fprintln(w, "Denied per day: none")
		return
	}
	fmt.Fprintln(w, "Denied per day")
	for _, d := range denied {
		fmt.Fprintf(w, "  %s  %d%s, %s\n", d.Day, d.Total, deniedWhat(d.Write, d.Shell), plural(d.Turns, "turn"))
	}
}

// deniedWhat is " (2 writes, 1 shell command)", leaving out what is zero.
func deniedWhat(write, shell int) string {
	var what []string
	if write > 0 {
		what = append(what, plural(write, "write"))
	}
	if shell > 0 {
		what = append(what, plural(shell, "shell command"))
	}
	if len(what) == 0 {
		return ""
	}
	return " (" + strings.Join(what, ", ") + ")"
}

// printSkipped names the lines left out, and why.
func printSkipped(w io.Writer, c stats.LogCounts) {
	for _, s := range []struct {
		n    int
		what string
	}{
		{c.Unverified, "not signed on this machine (edited, torn, or made elsewhere)"},
		{c.OtherProject, "from another project"},
		{c.Newer, "in a newer format: upgrade stele"},
		{c.UnknownKind, "of a kind this stele does not know"},
		{c.Future, "dated in the future"},
	} {
		if s.n > 0 {
			fmt.Fprintf(w, "%s skipped: %s\n", plural(s.n, "log line"), s.what)
		}
	}
}

// agentLabel is an agent's name for a person: the adapter's own words for
// the agent stele runs with, else its id as logged.
func agentLabel(id string) string {
	if id == agent.Name() {
		return agent.Words().Agent
	}
	return id
}
