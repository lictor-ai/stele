package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/stats"
)

// statsAt sets the clock and the zone stele stats counts days in.
func statsAt(t *testing.T, at time.Time) {
	t.Helper()
	clock(t, at)
	old := statsLoc
	statsLoc = time.UTC
	t.Cleanup(func() { statsLoc = old })
}

var statsNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// logLines appends lines to root's log as the hook would, signed with the
// test's key.
func logLines(t *testing.T, root string, rs ...journal.Record) {
	t.Helper()
	key, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if err := journal.Append(root, key, r); err != nil {
			t.Fatal(err)
		}
	}
}

func statsHeader(t *testing.T, root, agentName, session string, at time.Time) journal.Header {
	return journal.Header{Project: projectID(t, root), TS: at, Session: session, Agent: agentName}
}

func compiled(t *testing.T, steleMD string) string {
	t.Helper()
	root := project(t, steleMD)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d: %s", code, errOut)
	}
	return root
}

func statsJSON(t *testing.T, args ...string) stats.Report {
	t.Helper()
	code, out, errOut := runCmd(append([]string{"stats", "--format", "json"}, args...)...)
	if code != exitOK {
		t.Fatalf("stats = %d: %s", code, errOut)
	}
	var rep stats.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	return rep
}

func TestStatsNoRuleFiles(t *testing.T) {
	project(t, "")
	code, out, _ := runCmd("stats")
	if code != exitOK || !strings.Contains(out, "no rules to report on") {
		t.Errorf("stats = %d, %q", code, out)
	}
}

// Before the hooks ever ran there is no log, and stats makes no key.
func TestStatsNoLog(t *testing.T) {
	project(t, rules)
	statsAt(t, statsNow)
	code, out, errOut := runCmd("stats")
	if code != exitOK || !strings.Contains(out, "no log yet") {
		t.Errorf("stats = %d, %q, %q", code, out, errOut)
	}
	dir, _ := keyDir()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("stats made a key: %v", entries)
	}
}

func TestStatsBadArgs(t *testing.T) {
	compiled(t, rules)
	for _, args := range [][]string{{"--format", "xml"}, {"--since", "3x"}, {"--since", "0d"}, {"--since", "2562048h"}, {"--since", "2999-01-01"}, {"extra"}} {
		code, _, errOut := runCmd(append([]string{"stats"}, args...)...)
		if code != exitCantRun || !strings.HasPrefix(errOut, "stele stats: ") {
			t.Errorf("stats %v = %d, %q; want 2", args, code, errOut)
		}
	}
}

// The real hook's lines: a write that broke a rule, then the agent's clean
// rewrite in the same turn, read back as broken once and fixed once.
func TestStatsEndToEndHook(t *testing.T) {
	root := project(t, checkRules)
	statsAt(t, time.Now().Add(time.Minute))
	postHook(t, root, "post-write", "internal/db.go", "package db\n\nvar password = 1\n")
	postHook(t, root, "post-write", "internal/db.go", "package db\n")
	rep := statsJSON(t)
	if rep.V != stats.ReportVersion || rep.Project != projectID(t, root) {
		t.Errorf("v %d, project %q", rep.V, rep.Project)
	}
	if rep.All.Writes != 2 || rep.All.WritesChecked != 2 || rep.All.Turns != 1 || rep.All.Enough {
		t.Errorf("all = %+v, want 2 writes in 1 turn, not enough", rep.All)
	}
	if len(rep.All.Broken) != 1 || rep.All.Broken[0].Writes != 1 || rep.All.Broken[0].Fixed != 1 || rep.All.Broken[0].Excerpt == "" {
		t.Errorf("broken = %+v, want one rule broken once and fixed", rep.All.Broken)
	}
	_, out, _ := runCmd("stats")
	for _, want := range []string{"Too little data to judge the rules: 1 turn with a write logged, stats need 30.", "fixed 1 of 1", `"Never put credentials`} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

const statsMD = `scope: ["**/*.go"]

Never build SQL by string concatenation.

---

scope: ["migrations/**"]

Migrations are append-only.
`

// enough logs 40 turns of Go writes, a deny a day for two days, and one
// rule broken and fixed.
func enough(t *testing.T, root string) {
	t.Helper()
	cc := agent.Name()
	var rs []journal.Record
	start := statsNow.AddDate(0, 0, -5)
	rs = append(rs, &journal.LoadedRecord{Header: statsHeader(t, root, cc, "s1", start), Rules: ruleIDsOf(t, root)})
	for i := range 40 {
		at := start.Add(time.Duration(i) * time.Hour)
		rs = append(rs, &journal.WroteRecord{Header: statsHeader(t, root, cc, "s1", at), Prompt: "u" + string(rune('A'+i)), File: "a.go"})
	}
	rs = append(rs,
		&journal.DeniedRecord{Header: statsHeader(t, root, cc, "s2", statsNow.AddDate(0, 0, -2)), Prompt: "d1", Via: "write", Files: []string{"b.go"}},
		&journal.DeniedRecord{Header: statsHeader(t, root, cc, "s2", statsNow.AddDate(0, 0, -1)), Prompt: "d2", Via: "shell", Files: []string{"b.go"}})
	logLines(t, root, rs...)
}

// ruleIDsOf is the ids in stele.lock, in order.
func ruleIDsOf(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "stele.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var lf struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &lf); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range lf.Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestStatsText(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	code, out, errOut := runCmd("stats")
	if code != exitOK || errOut != "" {
		t.Fatalf("stats = %d, stderr %q", code, errOut)
	}
	ids := ruleIDsOf(t, root)
	for _, want := range []string{
		"last 30 days, 2026-10-04 to 2026-11-03 (UTC), from .stele/log.jsonl",
		"2 sessions, 42 turns with a write, 40 writes (0 checked), 2 denied (1 write, 1 shell command)",
		"Broken after a write: none",
		"Dead: no write met a file they apply to, in 30 turns or more",
		ids[1] + ` "Migrations are append-only." scope: migrations/**`,
		"Denied per day",
		"2026-11-01  1 (1 write), 1 turn",
		"2026-11-02  1 (1 shell command), 1 turn",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"Too little data", "By agent", ids[0]} {
		if strings.Contains(out, not) {
			t.Errorf("text has %q:\n%s", not, out)
		}
	}
}

func TestStatsJSON(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	rep := statsJSON(t, "--since", "all")
	if rep.Since != nil || !rep.All.Enough || rep.All.Turns != 42 || len(rep.All.Dead) != 1 || rep.TZ != "UTC" || !rep.RulesJudged {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Agent != agent.Name() {
		t.Errorf("agents = %+v", rep.Agents)
	}
}

// A window that starts after every line: the log is there, the window is
// empty.
func TestStatsEmptyWindow(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	code, out, _ := runCmd("stats", "--since", "12h")
	if code != exitOK || !strings.Contains(out, "nothing logged in the last 12 hours (43 lines before)") {
		t.Errorf("stats = %d:\n%s", code, out)
	}
}

func TestStatsTwoAgents(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	logLines(t, root, &journal.WroteRecord{Header: statsHeader(t, root, "codex", "c1", statsNow.Add(-time.Hour)), Prompt: "x", File: "a.go"})
	_, out, _ := runCmd("stats")
	for _, want := range []string{"By agent", "  " + agent.Words().Agent + ": 2 sessions, 42 turns, 2 denied, 0 broken, 1 dead", "  codex: 1 session, 1 turn: too little data"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

// Lines this machine did not sign are counted and named, never used.
func TestStatsUnverifiedLines(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	f, err := os.OpenFile(filepath.Join(root, ".stele", journal.FileName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"v":1,"kind":"denied","project":"x"}` + "\n")
	f.Close()
	_, out, _ := runCmd("stats")
	if !strings.Contains(out, "1 log line skipped: not signed on this machine") {
		t.Errorf("text:\n%s", out)
	}
}

// stats reads; it writes nothing, not even a line of the log about itself.
func TestStatsWritesNothing(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	before := tree(t, root)
	runCmd("stats")
	runCmd("stats", "--format", "json")
	if after := tree(t, root); after != before {
		t.Errorf("stats changed the project:\nbefore %s\nafter  %s", before, after)
	}
}

// tree lists every file under root with its size and time.
func tree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil // reading takes the log's lock, a file made and removed
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s %v %d\n", filepath.ToSlash(rel), info.ModTime(), info.Size())
		return nil
	})
	return b.String()
}

// A rule stele.lock does not have yet is new, never dead; stats says the
// lock is stale.
func TestStatsStaleLock(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(statsMD+"\n---\n\nscope: [docs/**]\n\nDocs are in English.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd("stats")
	if code != exitOK || !strings.Contains(errOut, "stele.lock is out of date with the rules: run stele compile") {
		t.Errorf("stats = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "1 rule new: no session has loaded it yet") || strings.Contains(out, "Docs are in English.\" scope") {
		t.Errorf("text:\n%s", out)
	}
}

// A stele.md that stops parsing still has the last good rules judged.
func TestStatsBrokenRules(t *testing.T) {
	root := compiled(t, statsMD)
	statsAt(t, statsNow)
	enough(t, root)
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte("tags: [a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd("stats")
	if code != exitOK || errOut == "" || !strings.Contains(out, "Migrations are append-only.") {
		t.Errorf("stats = %d, stderr %q:\n%s", code, errOut, out)
	}
}
