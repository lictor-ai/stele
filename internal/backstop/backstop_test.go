package backstop

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/session"
)

var (
	t0      = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	sqlRule = Rule{ID: "sql", Text: "Never build SQL by string concatenation."}
	logRule = Rule{ID: "log", Text: "Log through the shared logger."}
	on      = enforce.Policy{Backstop: true}
)

func shown(agent string, at time.Time, ids ...string) map[string]map[string]time.Time {
	m := map[string]time.Time{}
	for _, id := range ids {
		m[id] = at
	}
	return map[string]map[string]time.Time{agent: m}
}

// denied is shown's twin for the files whose write was denied.
func denied(agent string, at time.Time, files ...string) map[string]map[string]time.Time {
	return shown(agent, at, files...)
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		in         Input
		st         session.State
		p          enforce.Policy
		wantDeny   bool
		wantRules  []Rule
		wantShown  map[string]map[string]time.Time
		wantDenied map[string]map[string]time.Time
	}{
		{
			name:       "first write of a turn shows the rules",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule, logRule}, Now: t0},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule, logRule},
			wantShown:  shown("", t0, "sql", "log"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "after the window, the retry goes through",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(10 * time.Second)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "within the window, a retry of the denied file goes through",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(2 * time.Second)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "a parallel write to the denied file, sooner than RetryAfter, is denied too",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(50 * time.Millisecond)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule},
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0.Add(50*time.Millisecond), "a.go"),
		},
		{
			name:       "a write to the denied file at exactly RetryAfter is a retry",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(RetryAfter)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "within the window, a parallel write to another file is denied too",
			in:         Input{PromptID: "p1", File: "b.go", Rules: []Rule{sqlRule}, Now: t0.Add(time.Second)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule},
			wantShown:  shown("", t0, "sql"), // not re-stamped: the window does not slide
			wantDenied: map[string]map[string]time.Time{"": {"a.go": t0, "b.go": t0.Add(time.Second)}},
		},
		{
			name:       "a retry that needs a rule never shown is denied with only that rule",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule, logRule}, Now: t0.Add(2 * time.Second)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{logRule},
			wantShown:  map[string]map[string]time.Time{"": {"sql": t0, "log": t0.Add(2 * time.Second)}},
			wantDenied: denied("", t0.Add(2*time.Second), "a.go"),
		},
		{
			name: "a retry needing a rule shown after its deny, still in the window, is denied with it",
			in:   Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule, logRule}, Now: t0.Add(2 * time.Second)},
			st: session.State{
				PromptID: "p1",
				Shown:    map[string]map[string]time.Time{"": {"sql": t0, "log": t0.Add(time.Second)}},
				Denied:   map[string]map[string]time.Time{"": {"a.go": t0, "b.go": t0.Add(time.Second)}},
			},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{logRule},
			wantShown:  map[string]map[string]time.Time{"": {"sql": t0, "log": t0.Add(time.Second)}},
			wantDenied: map[string]map[string]time.Time{"": {"a.go": t0.Add(2 * time.Second), "b.go": t0.Add(time.Second)}},
		},
		{
			name:      "a subagent writing the main agent's denied file is not a retry",
			in:        Input{PromptID: "p1", AgentID: "sub", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(time.Second)},
			st:        session.State{PromptID: "p1", Shown: shown("sub", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule},
			wantShown: shown("sub", t0, "sql"),
			wantDenied: map[string]map[string]time.Time{
				"":    {"a.go": t0},
				"sub": {"a.go": t0.Add(time.Second)},
			},
		},
		{
			name:       "only the rules not yet shown",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule, logRule}, Now: t0.Add(10 * time.Second)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{logRule},
			wantShown:  map[string]map[string]time.Time{"": {"sql": t0, "log": t0.Add(10 * time.Second)}},
			wantDenied: denied("", t0.Add(10*time.Second), "a.go"),
		},
		{
			name:       "a new turn starts with nothing shown and nothing denied",
			in:         Input{PromptID: "p2", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(time.Minute)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule},
			wantShown:  shown("", t0.Add(time.Minute), "sql"),
			wantDenied: denied("", t0.Add(time.Minute), "a.go"),
		},
		{
			name:       "a subagent has its own record",
			in:         Input{PromptID: "p1", AgentID: "sub", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(time.Minute)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule},
			wantShown:  map[string]map[string]time.Time{"": {"sql": t0}, "sub": {"sql": t0.Add(time.Minute)}},
			wantDenied: denied("sub", t0.Add(time.Minute), "a.go"),
		},
		{
			name:       "the clock moved back: a rule shown in the future counts as read",
			in:         Input{PromptID: "p1", File: "b.go", Rules: []Rule{sqlRule}, Now: t0.Add(-time.Hour)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "the clock moved back on a retry",
			in:         Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(-time.Hour)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:       "at the same instant, another file is in the window",
			in:         Input{PromptID: "p1", File: "b.go", Rules: []Rule{sqlRule}, Now: t0},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantDeny:   true,
			wantRules:  []Rule{sqlRule},
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go", "b.go"),
		},
		{
			name:       "at exactly the window, another file goes through",
			in:         Input{PromptID: "p1", File: "b.go", Rules: []Rule{sqlRule}, Now: t0.Add(Window)},
			st:         session.State{PromptID: "p1", Shown: shown("", t0, "sql"), Denied: denied("", t0, "a.go")},
			p:          on,
			wantShown:  shown("", t0, "sql"),
			wantDenied: denied("", t0, "a.go"),
		},
		{
			name:      "with no file, there is no retry and nothing is recorded",
			in:        Input{PromptID: "p1", Rules: []Rule{sqlRule}, Now: t0.Add(time.Second)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule},
			wantShown: shown("", t0, "sql"),
		},
		{
			name: "no rules apply",
			in:   Input{PromptID: "p1", File: "a.go", Now: t0},
			p:    on,
		},
		{
			name: "backstop off",
			in:   Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0},
			p:    enforce.Policy{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneState(tt.st)
			d, next := Decide(tt.in, tt.st, tt.p)
			if d.Deny != tt.wantDeny {
				t.Errorf("Deny = %v, want %v", d.Deny, tt.wantDeny)
			}
			if diff := cmp.Diff(tt.wantRules, d.Rules, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("rules (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantShown, next.Shown, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("shown (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantDenied, next.Denied, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("denied (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, tt.st); diff != "" {
				t.Errorf("Decide changed the caller's state (-before +after):\n%s", diff)
			}
		})
	}
}

// cloneState deep-copies the maps, so a write through st's maps shows up as
// a difference from the copy.
func cloneState(st session.State) session.State {
	cp := func(m map[string]map[string]time.Time) map[string]map[string]time.Time {
		if m == nil {
			return nil
		}
		out := make(map[string]map[string]time.Time, len(m))
		for k, v := range m {
			inner := make(map[string]time.Time, len(v))
			for kk, vv := range v {
				inner[kk] = vv
			}
			out[k] = inner
		}
		return out
	}
	st.Shown = cp(st.Shown)
	st.Denied = cp(st.Denied)
	return st
}

// A deny dated in the future (the clock moved back since) is not a retry, so
// a rule shown since, and not read, is not let through by it.
func TestDecideDenyInTheFuture(t *testing.T) {
	r1 := Rule{ID: "r1", Text: "one"}
	r2 := Rule{ID: "r2", Text: "two"}
	nine := t0
	eight := t0.Add(-time.Hour)
	var st session.State
	decide := func(file string, at time.Time, rules ...Rule) Decision {
		t.Helper()
		var d Decision
		d, st = Decide(Input{PromptID: "p1", File: file, Rules: rules, Now: at}, st, on)
		return d
	}
	if d := decide("a.go", nine, r1); !d.Deny {
		t.Fatal("a.go at 9:00: allowed, want denied with r1")
	}
	// The clock moves back to 8:00, and r2 is added, covering a.go and c.go.
	if d := decide("c.go", eight, r2); !d.Deny {
		t.Fatal("c.go at 8:00: allowed, want denied with r2")
	}
	d := decide("a.go", eight.Add(100*time.Millisecond), r1, r2)
	if diff := cmp.Diff([]Rule{r2}, d.Rules, cmpopts.EquateEmpty()); !d.Deny || diff != "" {
		t.Errorf("a.go at 8:00:00.1: deny %v, rules (-want +got):\n%s", d.Deny, diff)
	}
}

// TestDecideBatch chains Decide over a batch of parallel writes and their
// retries, as the hook sees them.
func TestDecideBatch(t *testing.T) {
	var st session.State
	write := func(file string, at time.Duration) bool {
		t.Helper()
		var d Decision
		d, st = Decide(Input{PromptID: "p1", File: file, Rules: []Rule{sqlRule}, Now: t0.Add(at)}, st, on)
		return d.Deny
	}
	steps := []struct {
		file     string
		at       time.Duration
		wantDeny bool
	}{
		{"a.go", 0, true},                        // the first write shows the rule
		{"a.go", 50 * time.Millisecond, true},    // a parallel write to the same file
		{"b.go", 200 * time.Millisecond, true},   // a parallel write, before the reason was read
		{"a.go", 2 * time.Second, false},         // the retry of a
		{"b.go", 2100 * time.Millisecond, false}, // the retry of b
		// A first write to c while the window is open is denied once: known
		// gap, bounded by the window.
		{"c.go", 2200 * time.Millisecond, true},
		{"c.go", 4 * time.Second, false},
	}
	for _, s := range steps {
		if got := write(s.file, s.at); got != s.wantDeny {
			t.Errorf("write %s at +%v: deny = %v, want %v", s.file, s.at, got, s.wantDeny)
		}
	}
}
