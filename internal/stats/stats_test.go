package stats

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/sign"
)

var (
	testKey = sign.Key(bytes.Repeat([]byte{7}, 32))
	until   = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	since   = until.AddDate(0, 0, -30)
	plusOne = time.FixedZone("plus1", 3600)
)

func input(rules ...Rule) Input {
	return Input{Project: "p1", Since: since, Until: until, Loc: time.UTC, Rules: rules, RulesKnown: true}
}

// at is a time n minutes after the window starts.
func at(n int) time.Time { return since.Add(time.Duration(n) * time.Minute) }

func wrote(ts time.Time, session, prompt, file string, rules ...string) Line {
	return Line{Kind: journal.Wrote, Project: "p1", TS: ts, Session: session, Agent: "claude-code", Prompt: prompt, File: file, Rules: rules}
}

func checked(ts time.Time, session, prompt, file string, rules []string, broken ...string) Line {
	l := Line{Kind: journal.Checked, Project: "p1", TS: ts, Session: session, Agent: "claude-code", Prompt: prompt, File: file, Rules: rules}
	for i, r := range broken {
		l.Violations = append(l.Violations, journal.Violation{Rule: r, Line: i + 1, Blocking: true})
	}
	return l
}

func denied(ts time.Time, session, prompt, via string, files, rules []string) Line {
	return Line{Kind: journal.Denied, Project: "p1", TS: ts, Session: session, Agent: "claude-code", Prompt: prompt, Via: via, Files: files, Rules: rules}
}

func loaded(ts time.Time, session string, rules ...string) Line {
	return Line{Kind: journal.Loaded, Project: "p1", TS: ts, Session: session, Agent: "claude-code", Rules: rules}
}

// turns is n writes, each its own turn, to file.
func turns(n int, file string, rules ...string) []Line {
	var ls []Line
	for i := range n {
		ls = append(ls, wrote(at(10+i), "s1", "u"+string(rune('a'+i%26))+strings.Repeat("x", i/26), file, rules...))
	}
	return ls
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, plusOne)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"30d", now.AddDate(0, 0, -30)},
		{"12h", now.Add(-12 * time.Hour)},
		{"2w", now.AddDate(0, 0, -14)},
		{"2026-10-01", time.Date(2026, 10, 1, 0, 0, 0, 0, plusOne)},
		{"all", time.Time{}},
	}
	for _, tt := range tests {
		got, err := ParseSince(tt.in, now, plusOne)
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "0d", "-1d", "3x", "d", "2026-11-04", "2026-13-01", "1.5d"} {
		if _, err := ParseSince(bad, now, plusOne); err == nil {
			t.Errorf("ParseSince(%q) took it", bad)
		}
	}
}

// project returns a root with .stele/, as the hook leaves it.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, index.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func appendLine(t *testing.T, root string, r journal.Record) {
	t.Helper()
	if err := journal.Append(root, testKey, r); err != nil {
		t.Fatal(err)
	}
}

func TestCollect(t *testing.T) {
	root := project(t)
	h := func(project string, ts time.Time) journal.Header {
		return journal.Header{Project: project, TS: ts, Session: "s1", Agent: "claude-code"}
	}
	appendLine(t, root, &journal.WroteRecord{Header: h("p1", at(1)), File: "a.go"})
	appendLine(t, root, &journal.WroteRecord{Header: h("p2", at(2)), File: "a.go"})              // another project
	appendLine(t, root, &journal.WroteRecord{Header: h("p1", since.Add(-time.Hour)), File: "a"}) // before the window
	appendLine(t, root, &journal.WroteRecord{Header: h("p1", until.Add(time.Hour)), File: "a"})  // in the future
	appendLine(t, root, &journal.CheckRunRecord{Header: journal.Header{Project: "p1", TS: at(3)}, Mode: "all"})
	log := filepath.Join(root, index.Dir, journal.FileName)
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"v":1,"kind":"wrote","project":"p1"}` + "\n") // unsigned
	f.Close()

	lines, counts, err := Collect(root, testKey, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Kind != journal.Wrote || lines[0].File != "a.go" || lines[1].Kind != journal.CheckRun {
		t.Errorf("lines = %+v", lines)
	}
	want := LogCounts{Lines: 6, Used: 2, Unverified: 1, OtherProject: 1, Before: 1, Future: 1}
	if diff := cmp.Diff(want, counts); diff != "" {
		t.Errorf("counts (-want +got):\n%s", diff)
	}
}

func TestCollectNewerAndUnknown(t *testing.T) {
	in := input()
	var c LogCounts
	keep := func(body string) bool { _, ok := c.take(in, []byte(body)); return ok }
	if keep(`{"v":2,"kind":"wrote","project":"p1","ts":"2026-10-10T00:00:00Z"}`) || c.Newer != 1 {
		t.Errorf("a newer line was used: %+v", c)
	}
	if keep(`{"v":1,"kind":"later","project":"p1","ts":"2026-10-10T00:00:00Z"}`) || c.UnknownKind != 1 {
		t.Errorf("an unknown kind was used: %+v", c)
	}
}

func TestCollectNoLog(t *testing.T) {
	lines, counts, err := Collect(t.TempDir(), testKey, input())
	if err != nil || len(lines) != 0 || counts != (LogCounts{}) {
		t.Errorf("no .stele: %v, %+v, %v", lines, counts, err)
	}
}

func TestTurnsAreSessionPromptPairs(t *testing.T) {
	ls := []Line{
		wrote(at(1), "s1", "u1", "a.go"),
		checked(at(1), "s1", "u1", "a.go", nil),
		wrote(at(2), "s1", "u2", "a.go"),
		denied(at(3), "s2", "u1", "write", []string{"a.go"}, []string{"r1"}),
		wrote(at(4), "s3", "", "a.go"),
		wrote(at(5), "s3", "", "b.go"),
		loaded(at(0), "s4"),
	}
	rep := Compute(input(), ls, LogCounts{})
	if rep.All.Turns != 4 || rep.All.Sessions != 4 || rep.All.Writes != 4 || rep.All.WritesChecked != 1 {
		t.Errorf("all = %+v, want 4 turns, 4 sessions, 4 writes, 1 checked", rep.All)
	}
}

func TestTooLittleData(t *testing.T) {
	rep := Compute(input(Rule{ID: "r1", Scope: []string{"x/**"}}), turns(MinTurns-1, "a.go"), LogCounts{})
	if rep.All.Enough || len(rep.All.Dead) != 0 || len(rep.All.NotJudged) != 1 {
		t.Errorf("all = %+v, want not enough and r1 not judged", rep.All)
	}
	rep = Compute(input(Rule{ID: "r1", Scope: []string{"x/**"}}), turns(MinTurns, "a.go"), LogCounts{})
	if !rep.All.Enough || len(rep.All.Dead) != 1 || rep.All.Dead[0].RuleID != "r1" || rep.All.Dead[0].TurnsSince != MinTurns {
		t.Errorf("all = %+v, want r1 dead after %d turns", rep.All, MinTurns)
	}
}

func TestDeadRuleMetByScope(t *testing.T) {
	ls := append(turns(MinTurns, "a.go"), wrote(at(100), "s1", "z", "x/y.go"))
	rep := Compute(input(Rule{ID: "r1", Scope: []string{"x/**"}}, Rule{ID: "r2", Scope: []string{"none/**"}}), ls, LogCounts{})
	if len(rep.All.Dead) != 1 || rep.All.Dead[0].RuleID != "r2" {
		t.Errorf("dead = %+v, want only r2", rep.All.Dead)
	}
}

// A rule whose scope changed since is met by its id on an older line.
func TestDeadRuleMetByID(t *testing.T) {
	ls := append(turns(MinTurns, "a.go"), denied(at(100), "s1", "z", "write", []string{"old/a.go"}, []string{"r1"}))
	rep := Compute(input(Rule{ID: "r1", Scope: []string{"new/**"}}), ls, LogCounts{})
	if len(rep.All.Dead) != 0 {
		t.Errorf("dead = %+v, want none: r1 was shown", rep.All.Dead)
	}
}

func TestUnscopedRuleMetByAnyWrite(t *testing.T) {
	rep := Compute(input(Rule{ID: "r1"}), turns(MinTurns, "a.go"), LogCounts{})
	if len(rep.All.Dead) != 0 {
		t.Errorf("dead = %+v, want none", rep.All.Dead)
	}
}

// A rule no session loaded yet is new: its turns have not started.
func TestRuleNotLoadedYetIsNew(t *testing.T) {
	ls := append([]Line{loaded(at(0), "s1", "r1")}, turns(MinTurns, "a.go")...)
	rules := []Rule{{ID: "r1", Scope: []string{"x/**"}}, {ID: "r2", Scope: []string{"x/**"}}, {ID: "r3", Scope: []string{"x/**"}, New: true}}
	rep := Compute(input(rules...), ls, LogCounts{})
	if len(rep.All.Dead) != 1 || rep.All.Dead[0].RuleID != "r1" {
		t.Errorf("dead = %+v, want r1", rep.All.Dead)
	}
	if len(rep.All.New) != 2 || rep.All.New[0].RuleID != "r2" || rep.All.New[1].RuleID != "r3" {
		t.Errorf("new = %+v, want r2 and r3", rep.All.New)
	}
}

// Turns count from the first session that loaded the rule.
func TestDeadCountsTurnsSinceLoaded(t *testing.T) {
	ls := append(turns(MinTurns, "a.go"), loaded(at(1000), "s9", "r1"), wrote(at(1001), "s9", "u", "a.go"))
	rep := Compute(input(Rule{ID: "r1", Scope: []string{"x/**"}}), ls, LogCounts{})
	if len(rep.All.Dead) != 0 || len(rep.All.NotJudged) != 1 || rep.All.NotJudged[0].TurnsSince != 1 {
		t.Errorf("all = %+v, want r1 not judged, 1 turn since it loaded", rep.All)
	}
}

func TestDeadNotJudgedWithoutRules(t *testing.T) {
	in := input()
	in.RulesKnown = false
	rep := Compute(in, turns(MinTurns, "a.go"), LogCounts{})
	if rep.RulesJudged || len(rep.All.Dead) != 0 {
		t.Errorf("report = %+v, want dead not judged", rep)
	}
}

func TestFixedNextCleanCheckSameTurn(t *testing.T) {
	ls := []Line{
		checked(at(1), "s1", "u1", "a.go", []string{"r1"}, "r1", "r1"),
		checked(at(2), "s1", "u1", "a.go", []string{"r1"}),
	}
	rep := Compute(input(Rule{ID: "r1", Excerpt: "No r1."}), ls, LogCounts{})
	want := []Broken{{RuleID: "r1", Excerpt: "No r1.", InRules: true, Writes: 1, Violations: 2, Blocking: 2, Fixed: 1}}
	if diff := cmp.Diff(want, rep.All.Broken); diff != "" {
		t.Errorf("broken (-want +got):\n%s", diff)
	}
}

// Only the next check of the same file, by the same agent, in the same turn,
// that checked the rule, says whether it was fixed.
func TestFixedIgnoresOtherChecks(t *testing.T) {
	sub := checked(at(2), "s1", "u1", "a.go", []string{"r1"})
	sub.Subagent = "a1"
	ls := []Line{
		checked(at(1), "s1", "u1", "a.go", []string{"r1"}, "r1"),
		checked(at(2), "s1", "u1", "b.go", []string{"r1"}),
		checked(at(2), "s2", "u1", "a.go", []string{"r1"}),
		sub,
		checked(at(3), "s1", "u1", "a.go", []string{"r2"}),
		checked(at(4), "s1", "u2", "a.go", []string{"r1"}),
	}
	rep := Compute(input(Rule{ID: "r1"}), ls, LogCounts{})
	if len(rep.All.Broken) != 1 || rep.All.Broken[0].Left != 1 || rep.All.Broken[0].Fixed != 0 {
		t.Errorf("broken = %+v, want left 1", rep.All.Broken)
	}
}

func TestBrokenAgainThenFixed(t *testing.T) {
	ls := []Line{
		checked(at(1), "s1", "u1", "a.go", []string{"r1"}, "r1"),
		checked(at(2), "s1", "u1", "a.go", []string{"r1"}, "r1"),
		checked(at(3), "s1", "u1", "a.go", []string{"r1"}),
	}
	rep := Compute(input(Rule{ID: "r1"}), ls, LogCounts{})
	b := rep.All.Broken[0]
	if b.Writes != 2 || b.Fixed != 1 || b.Again != 1 || b.Left != 0 {
		t.Errorf("broken = %+v, want 2 writes, fixed 1, again 1", b)
	}
}

// Before the first prompt a turn has no id: the same session is enough.
func TestFixedWithoutPrompt(t *testing.T) {
	ls := []Line{
		checked(at(1), "s1", "", "a.go", []string{"r1"}, "r1"),
		checked(at(2), "s1", "u1", "a.go", []string{"r1"}),
	}
	rep := Compute(input(Rule{ID: "r1"}), ls, LogCounts{})
	if rep.All.Broken[0].Fixed != 1 {
		t.Errorf("broken = %+v, want fixed", rep.All.Broken)
	}
}

func TestBrokenOrderAndCheckRuns(t *testing.T) {
	ls := []Line{
		checked(at(1), "s1", "u1", "a.go", []string{"r1", "r2"}, "r2"),
		checked(at(2), "s1", "u2", "a.go", []string{"r1", "r2"}, "r2"),
		checked(at(3), "s1", "u3", "a.go", []string{"r1", "r2"}, "r1"),
		{Kind: journal.CheckRun, Project: "p1", TS: at(4), Errors: 3, Broken: map[string]int{"r3": 3}},
		{Kind: journal.CheckRun, Project: "p1", TS: at(5)},
		checked(at(6), "s1", "u4", "a.go", []string{"gone"}, "gone"),
	}
	rep := Compute(input(Rule{ID: "r1"}, Rule{ID: "r2"}, Rule{ID: "r3"}), ls, LogCounts{})
	var ids []string
	for _, b := range rep.All.Broken {
		ids = append(ids, b.RuleID)
	}
	if strings.Join(ids, " ") != "r2 gone r1 r3" {
		t.Errorf("order = %v, want r2 gone r1 r3", ids)
	}
	last := rep.All.Broken[3]
	if last.Writes != 0 || last.InCheck != 3 {
		t.Errorf("r3 = %+v, want 3 in stele check", last)
	}
	if rep.All.Broken[1].InRules {
		t.Errorf("gone = %+v, want it marked not in the rules", rep.All.Broken[1])
	}
	if rep.CheckRuns.Runs != 2 || rep.CheckRuns.WithViolations != 1 || rep.CheckRuns.Broken["r3"] != 3 {
		t.Errorf("check runs = %+v", rep.CheckRuns)
	}
}

func TestFixRateNeedsData(t *testing.T) {
	var ls []Line
	for i := range MinBroken {
		p := "u" + string(rune('a'+i))
		ls = append(ls, checked(at(2*i), "s1", p, "a.go", []string{"r1"}, "r1"), checked(at(2*i+1), "s1", p, "a.go", []string{"r1"}))
	}
	rep := Compute(input(Rule{ID: "r1"}), ls, LogCounts{})
	if rep.All.Broken[0].FixRate != nil {
		t.Errorf("fix rate %v below %d turns", *rep.All.Broken[0].FixRate, MinTurns)
	}
	ls = append(ls, turns(MinTurns, "b.go")...)
	rep = Compute(input(Rule{ID: "r1"}), ls, LogCounts{})
	if r := rep.All.Broken[0].FixRate; r == nil || *r != 1 {
		t.Errorf("fix rate %v, want 1", r)
	}
}

func TestDeniedPerLocalDay(t *testing.T) {
	day := time.Date(2026, 10, 28, 23, 30, 0, 0, time.UTC) // 00:30 on the 29th at +1
	ls := []Line{
		denied(day, "s1", "u1", "write", []string{"a.go"}, []string{"r1"}),
		denied(day.Add(time.Minute), "s1", "u1", "shell", []string{"a.go"}, []string{"r1"}),
		denied(day.Add(-2*time.Hour), "s1", "u0", "write", []string{"a.go"}, []string{"r1"}),
	}
	in := input()
	in.Loc = plusOne
	rep := Compute(in, ls, LogCounts{})
	want := []Day{{Day: "2026-10-28", Total: 1, Write: 1, Turns: 1}, {Day: "2026-10-29", Total: 2, Write: 1, Shell: 1, Turns: 1}}
	if diff := cmp.Diff(want, rep.All.DeniedByDay); diff != "" {
		t.Errorf("by day (-want +got):\n%s", diff)
	}
	if rep.All.Denied != (Denied{Total: 3, Write: 2, Shell: 1}) {
		t.Errorf("denied = %+v", rep.All.Denied)
	}
	if rep.TZ != "plus1" {
		t.Errorf("tz = %q", rep.TZ)
	}
}

func TestAgents(t *testing.T) {
	cx := wrote(at(1), "c1", "u1", "a.go")
	cx.Agent = "codex"
	ls := []Line{wrote(at(1), "s1", "u1", "a.go"), wrote(at(2), "s1", "u2", "a.go"), cx,
		{Kind: journal.CheckRun, Project: "p1", TS: at(4)}}
	rep := Compute(input(), ls, LogCounts{})
	if len(rep.Agents) != 2 || rep.Agents[0].Agent != "claude-code" || rep.Agents[0].Turns != 2 || rep.Agents[1].Agent != "codex" || rep.Agents[1].Turns != 1 {
		t.Errorf("agents = %+v", rep.Agents)
	}
	if rep.All.Turns != 3 {
		t.Errorf("all turns = %d, want 3", rep.All.Turns)
	}
}

func TestReportJSON(t *testing.T) {
	rep := Compute(input(), nil, LogCounts{})
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"v":1`, `"project":"p1"`, `"dead":[]`, `"not_judged":[]`, `"new":[]`, `"broken":[]`, `"denied_by_day":[]`, `"agents":[]`, `"need":{"turns":30,"broken":10}`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON lacks %s:\n%s", want, s)
		}
	}
	in := input()
	in.Since = time.Time{}
	data, _ = json.Marshal(Compute(in, nil, LogCounts{}))
	if !strings.Contains(string(data), `"since":null`) {
		t.Errorf("since all: %s", data)
	}
}
