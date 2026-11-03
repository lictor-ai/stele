package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/sign"
)

const (
	// ReportVersion is the version of Report's JSON (D50).
	ReportVersion = 1
	// MinTurns is how many turns stats needs before it judges anything: under
	// a few dozen turns the numbers are noise (O-LOG-VOLUME).
	MinTurns = 30
	// MinBroken is how many broken writes a rule needs before its fix rate is
	// given.
	MinBroken = 10
)

// Rule is a rule of the current rules, as stats reports it.
type Rule struct {
	ID      string
	Excerpt string
	Scope   []string
	// New is a rule stele.lock does not have yet: its id is not settled, so
	// it is never judged.
	New bool
}

// Input is what the report covers.
type Input struct {
	Project string    // the lock's project: lines of another are left out; "" keeps all
	Since   time.Time // zero: from the first line
	Until   time.Time
	Loc     *time.Location // for days
	Rules   []Rule
	// RulesKnown is false when the rules could not be read: no rule is then
	// judged dead.
	RulesKnown bool
}

// Line is one line of the log, every kind's fields together: no two kinds
// name a list and a count alike (ADR-0010).
type Line struct {
	V          int                 `json:"v"`
	Kind       journal.Kind        `json:"kind"`
	Project    string              `json:"project"`
	TS         time.Time           `json:"ts"`
	Session    string              `json:"session"`
	Agent      string              `json:"agent"`
	Subagent   string              `json:"subagent"`
	Prompt     string              `json:"prompt"`
	Via        string              `json:"via"`
	File       string              `json:"file"`
	Files      []string            `json:"files"`
	Rules      []string            `json:"rules"`
	Violations []journal.Violation `json:"violations"`
	Errors     int                 `json:"errors"`
	Warnings   int                 `json:"warnings"`
	Broken     map[string]int      `json:"broken"`
}

// LogCounts says what was read, and why lines were left out.
type LogCounts struct {
	Lines        int `json:"lines"`         // every line read
	Used         int `json:"used"`          // in the report
	Unverified   int `json:"unverified"`    // not signed on this machine, edited or torn
	OtherProject int `json:"other_project"` // another project's id
	Newer        int `json:"newer"`         // a newer format
	UnknownKind  int `json:"unknown_kind"`
	Before       int `json:"before"` // before the window
	Future       int `json:"future"` // dated after now
}

var known = map[journal.Kind]bool{journal.Loaded: true, journal.Denied: true, journal.Checked: true, journal.Wrote: true, journal.CheckRun: true}

// take decodes one signed line and says whether the report uses it,
// counting why not.
func (c *LogCounts) take(in Input, body []byte) (Line, bool) {
	c.Lines++
	var l Line
	switch {
	case json.Unmarshal(body, &l) != nil:
		c.Unverified++
	case l.V > journal.Version:
		c.Newer++
	case !known[l.Kind]:
		c.UnknownKind++
	case in.Project != "" && l.Project != in.Project:
		c.OtherProject++
	case l.TS.After(in.Until):
		c.Future++
	case !in.Since.IsZero() && l.TS.Before(in.Since):
		c.Before++
	default:
		c.Used++
		return l, true
	}
	return Line{}, false
}

// Collect reads root's log, the lines key signed in the window, oldest
// first. A root with no log has no lines.
func Collect(root string, key sign.Key, in Input) ([]Line, LogCounts, error) {
	var c LogCounts
	var lines []Line
	bad, err := journal.Read(root, key, func(_ journal.Kind, body []byte) error {
		if l, ok := c.take(in, body); ok {
			lines = append(lines, l)
		}
		return nil
	})
	c.Lines += bad
	c.Unverified += bad
	return lines, c, err
}

var relative = regexp.MustCompile(`^([1-9][0-9]*)([hdw])$`)

// ErrSince is a --since stats cannot read.
var ErrSince = errors.New("is like 30d, 12h, 2w, 2026-10-01 or all")

// ParseSince reads a window's start: Nh, Nd or Nw before now, a day (its
// midnight in loc), or all, which is the zero time.
func ParseSince(s string, now time.Time, loc *time.Location) (time.Time, error) {
	if s == "all" {
		return time.Time{}, nil
	}
	if m := relative.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, ErrSince
		}
		switch m[2] {
		case "h":
			return now.Add(-time.Duration(n) * time.Hour), nil
		case "d":
			return now.AddDate(0, 0, -n), nil
		default:
			return now.AddDate(0, 0, -7*n), nil
		}
	}
	day, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, ErrSince
	}
	if day.After(now) {
		return time.Time{}, fmt.Errorf("%s is in the future", s)
	}
	return day, nil
}

// Report is what stats found. Every list is empty, never null.
type Report struct {
	V       int        `json:"v"`
	Project string     `json:"project"`
	Since   *time.Time `json:"since"` // UTC; null: from the first line
	Until   time.Time  `json:"until"` // UTC
	TZ      string     `json:"tz"`    // the zone days are counted in
	Log     LogCounts  `json:"log"`
	Need    Need       `json:"need"`
	// RulesJudged is false when the rules could not be read: no rule is
	// called dead.
	RulesJudged bool           `json:"rules_judged"`
	All         Numbers        `json:"all"`
	Agents      []AgentNumbers `json:"agents"`
	CheckRuns   CheckRuns      `json:"check_runs"`
}

// Need is the data stats needs before it judges.
type Need struct {
	Turns  int `json:"turns"`
	Broken int `json:"broken"`
}

// Numbers are the counts for one agent, or all of them.
type Numbers struct {
	// Enough is whether there were MinTurns turns: below that the counts
	// are what happened, not a trend.
	Enough        bool      `json:"enough"`
	Sessions      int       `json:"sessions"`
	Turns         int       `json:"turns"` // user turns with a write stele saw
	Writes        int       `json:"writes"`
	WritesChecked int       `json:"writes_checked"`
	Denied        Denied    `json:"denied"`
	DeniedByDay   []Day     `json:"denied_by_day"`
	Broken        []Broken  `json:"broken"`
	Dead          []RuleRef `json:"dead"`
	NotJudged     []RuleRef `json:"not_judged"`
	New           []RuleRef `json:"new"`
}

// AgentNumbers are one agent's numbers, by its adapter's name.
type AgentNumbers struct {
	Agent string `json:"agent"`
	Numbers
}

// Denied counts denies, by what was denied.
type Denied struct {
	Total int `json:"total"`
	Write int `json:"write"`
	Shell int `json:"shell"`
}

// Day is one day with a deny or a turn.
type Day struct {
	Day   string `json:"day"` // YYYY-MM-DD in TZ
	Total int    `json:"total"`
	Write int    `json:"write"`
	Shell int    `json:"shell"`
	Turns int    `json:"turns"`
}

// Broken is a rule broken after a write, and what the agent did next.
type Broken struct {
	RuleID  string `json:"rule_id"`
	Excerpt string `json:"excerpt"`
	InRules bool   `json:"in_rules"` // false: the rule is gone from the rules
	// Writes is how many checked writes broke it; Violations and Blocking
	// count the lines.
	Writes     int `json:"writes"`
	Violations int `json:"violations"`
	Blocking   int `json:"blocking"`
	// After each: the agent's next check of the file in the turn was clean
	// (Fixed), broke it again (Again), or never came (Left).
	Fixed int `json:"fixed"`
	Again int `json:"again"`
	Left  int `json:"left"`
	// InCheck is the violations stele check found.
	InCheck int `json:"in_check"`
	// FixRate is Fixed over Writes, null below MinBroken writes or MinTurns.
	FixRate *float64 `json:"fix_rate"`
}

// RuleRef is a rule in the dead, not judged or new list.
type RuleRef struct {
	RuleID     string   `json:"rule_id"`
	Excerpt    string   `json:"excerpt"`
	Scope      []string `json:"scope"`
	TurnsSince int      `json:"turns_since"` // turns since a session first loaded it
}

// CheckRuns counts the stele check runs.
type CheckRuns struct {
	Runs           int            `json:"runs"`
	WithViolations int            `json:"with_violations"`
	Broken         map[string]int `json:"broken"` // rule id → violations found
}

// Compute is the report on lines, which Collect returned.
func Compute(in Input, lines []Line, counts LogCounts) Report {
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	rep := Report{V: ReportVersion, Project: in.Project, Until: in.Until.UTC(), TZ: zone(loc, in.Until),
		Log: counts, Need: Need{Turns: MinTurns, Broken: MinBroken}, RulesJudged: in.RulesKnown,
		Agents: []AgentNumbers{}, CheckRuns: CheckRuns{Broken: map[string]int{}}}
	if !in.Since.IsZero() {
		s := in.Since.UTC()
		rep.Since = &s
	}

	byAgent := map[string][]Line{}
	var agentLines []Line
	for _, l := range lines {
		if l.Kind == journal.CheckRun {
			rep.CheckRuns.Runs++
			if l.Errors+l.Warnings > 0 || len(l.Broken) > 0 {
				rep.CheckRuns.WithViolations++
			}
			for id, n := range l.Broken {
				rep.CheckRuns.Broken[id] += n
			}
			continue
		}
		agentLines = append(agentLines, l)
		byAgent[l.Agent] = append(byAgent[l.Agent], l)
	}
	rep.All = numbers(in, loc, agentLines)
	addCheckRuns(&rep.All, in, rep.CheckRuns.Broken)
	names := make([]string, 0, len(byAgent))
	for a := range byAgent {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		rep.Agents = append(rep.Agents, AgentNumbers{Agent: a, Numbers: numbers(in, loc, byAgent[a])})
	}
	return rep
}

// zone names loc: its name, or its offset when it has none to give.
func zone(loc *time.Location, at time.Time) string {
	if name := loc.String(); name != "Local" && name != "" {
		return name
	}
	return at.In(loc).Format("-07:00")
}

type turnKey struct{ session, prompt string }

// numbers are the counts on one agent's lines, or all agents'.
func numbers(in Input, loc *time.Location, lines []Line) Numbers {
	n := Numbers{DeniedByDay: []Day{}, Broken: []Broken{}, Dead: []RuleRef{}, NotJudged: []RuleRef{}, New: []RuleRef{}}
	sessions := map[string]bool{}
	turnStart := map[turnKey]time.Time{}
	days := map[string]*Day{}
	day := func(t time.Time) *Day {
		k := t.In(loc).Format("2006-01-02")
		if days[k] == nil {
			days[k] = &Day{Day: k}
		}
		return days[k]
	}
	for _, l := range lines {
		sessions[l.Session] = true
		switch l.Kind {
		case journal.Loaded:
			continue
		case journal.Wrote:
			n.Writes++
		case journal.Checked:
			n.WritesChecked++
		case journal.Denied:
			d := day(l.TS)
			n.Denied.Total++
			d.Total++
			if l.Via == "shell" {
				n.Denied.Shell++
				d.Shell++
			} else {
				n.Denied.Write++
				d.Write++
			}
		}
		k := turnKey{l.Session, l.Prompt}
		if t, ok := turnStart[k]; !ok || l.TS.Before(t) {
			turnStart[k] = l.TS
		}
	}
	n.Sessions, n.Turns = len(sessions), len(turnStart)
	n.Enough = n.Turns >= MinTurns
	for _, t := range turnStart {
		day(t).Turns++
	}
	for _, d := range days {
		n.DeniedByDay = append(n.DeniedByDay, *d)
	}
	sort.Slice(n.DeniedByDay, func(i, j int) bool { return n.DeniedByDay[i].Day < n.DeniedByDay[j].Day })

	n.Broken = broken(in, n.Enough, lines)
	if in.RulesKnown {
		dead(in, &n, lines, turnStart)
	}
	return n
}

// broken follows each rule a checked write broke to the agent's next check
// of that file in the same turn.
func broken(in Input, enough bool, lines []Line) []Broken {
	type fileKey struct{ session, subagent, file string }
	checks := map[fileKey][]int{} // positions of checked lines, in log order
	for i, l := range lines {
		if l.Kind == journal.Checked {
			k := fileKey{l.Session, l.Subagent, l.File}
			checks[k] = append(checks[k], i)
		}
	}
	excerpts := ruleMap(in)
	by := map[string]*Broken{}
	get := func(id string) *Broken {
		if by[id] == nil {
			r, ok := excerpts[id]
			by[id] = &Broken{RuleID: id, Excerpt: r.Excerpt, InRules: ok || !in.RulesKnown}
		}
		return by[id]
	}
	for _, pos := range checks {
		for at, i := range pos {
			l := lines[i]
			hits := map[string][2]int{} // rule → violations, blocking
			for _, v := range l.Violations {
				h := hits[v.Rule]
				h[0]++
				if v.Blocking {
					h[1]++
				}
				hits[v.Rule] = h
			}
			for id, h := range hits {
				b := get(id)
				b.Writes++
				b.Violations += h[0]
				b.Blocking += h[1]
				switch next := nextCheck(lines, pos[at+1:], l, id); {
				case next == nil:
					b.Left++
				case breaks(*next, id):
					b.Again++
				default:
					b.Fixed++
				}
			}
		}
	}
	out := []Broken{}
	for _, b := range by {
		if enough && b.Writes >= MinBroken {
			r := float64(b.Fixed) / float64(b.Writes)
			b.FixRate = &r
		}
		out = append(out, *b)
	}
	sortBroken(out)
	return out
}

// nextCheck is the first later check, of those at positions, in l's turn
// that checked rule id; nil when there is none.
func nextCheck(lines []Line, positions []int, l Line, id string) *Line {
	for _, j := range positions {
		n := lines[j]
		if n.Prompt != l.Prompt && n.Prompt != "" && l.Prompt != "" {
			continue // another turn
		}
		for _, r := range n.Rules {
			if r == id {
				return &lines[j]
			}
		}
	}
	return nil
}

func breaks(l Line, id string) bool {
	for _, v := range l.Violations {
		if v.Rule == id {
			return true
		}
	}
	return false
}

// addCheckRuns adds what stele check found to the broken rules.
func addCheckRuns(n *Numbers, in Input, found map[string]int) {
	if len(found) == 0 {
		return
	}
	rules := ruleMap(in)
	for id, c := range found {
		hit := false
		for j := range n.Broken {
			if n.Broken[j].RuleID == id {
				n.Broken[j].InCheck += c
				hit = true
			}
		}
		if !hit {
			r, ok := rules[id]
			n.Broken = append(n.Broken, Broken{RuleID: id, Excerpt: r.Excerpt, InRules: ok || !in.RulesKnown, InCheck: c})
		}
	}
	sortBroken(n.Broken)
}

// sortBroken puts the rules broken most after writes first, then those
// stele check found most, then by id.
func sortBroken(bs []Broken) {
	sort.Slice(bs, func(i, j int) bool {
		a, b := bs[i], bs[j]
		if a.Writes != b.Writes {
			return a.Writes > b.Writes
		}
		if a.InCheck != b.InCheck {
			return a.InCheck > b.InCheck
		}
		return a.RuleID < b.RuleID
	})
}

func ruleMap(in Input) map[string]Rule {
	m := make(map[string]Rule, len(in.Rules))
	for _, r := range in.Rules {
		m[r.ID] = r
	}
	return m
}

// dead sorts each rule no write met into dead, not judged or new. A rule is
// met by a write to a file its current scope applies to, or by its id on a
// line: its scope may have changed since. Its turns count from the first
// session that loaded it; with no session loading anything in the window,
// from the window's start.
func dead(in Input, n *Numbers, lines []Line, turnStart map[turnKey]time.Time) {
	met := map[string]bool{}
	var files []string
	firstLoaded := map[string]time.Time{}
	anyLoaded := false
	for _, l := range lines {
		switch l.Kind {
		case journal.Loaded:
			anyLoaded = true
			for _, id := range l.Rules {
				if t, ok := firstLoaded[id]; !ok || l.TS.Before(t) {
					firstLoaded[id] = l.TS
				}
			}
			continue
		case journal.Denied:
			files = append(files, l.Files...)
		default:
			files = append(files, l.File)
		}
		for _, id := range l.Rules {
			met[id] = true
		}
		for _, v := range l.Violations {
			met[v.Rule] = true
		}
	}
	files = unique(files)
	for _, r := range in.Rules {
		if met[r.ID] || meets(r, files) {
			continue
		}
		ref := RuleRef{RuleID: r.ID, Excerpt: r.Excerpt, Scope: append([]string{}, r.Scope...)}
		start, loaded := firstLoaded[r.ID]
		if r.New || (anyLoaded && !loaded) {
			n.New = append(n.New, ref)
			continue
		}
		if !anyLoaded {
			start = in.Since
		}
		for _, t := range turnStart {
			if !t.Before(start) {
				ref.TurnsSince++
			}
		}
		if ref.TurnsSince >= MinTurns {
			n.Dead = append(n.Dead, ref)
		} else {
			n.NotJudged = append(n.NotJudged, ref)
		}
	}
}

func meets(r Rule, files []string) bool {
	for _, f := range files {
		if scope.Applies(r.Scope, f) {
			return true
		}
	}
	return false
}

func unique(ss []string) []string {
	sort.Strings(ss)
	out := ss[:0]
	for i, s := range ss {
		if i == 0 || s != ss[i-1] {
			out = append(out, s)
		}
	}
	return out
}
