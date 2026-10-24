package backstop

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/session"
)

func TestDecideBash(t *testing.T) {
	aGo := BashFile{File: "a.go", Rules: []Rule{sqlRule, logRule}}
	bGo := BashFile{File: "b.go", Rules: []Rule{sqlRule}}
	tests := []struct {
		name           string
		in             BashInput
		st             session.State
		p              enforce.Policy
		want           BashDecision
		wantShown      map[string]map[string]time.Time
		wantDenied     map[string]map[string]time.Time
		wantBashDenied map[string]map[string]time.Time
	}{
		{
			name:           "first write through Bash is denied with the rules",
			in:             BashInput{PromptID: "p1", Files: []BashFile{aGo}, Now: t0},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"a.go"}, Rules: []Rule{sqlRule, logRule}},
			wantShown:      shown("", t0, "sql", "log"),
			wantDenied:     denied("", t0, "a.go"),
			wantBashDenied: denied("", t0, "a.go"),
		},
		{
			name: "run again after RetryAfter, it goes through",
			in:   BashInput{PromptID: "p1", Files: []BashFile{aGo}, Now: t0.Add(RetryAfter)},
			st: session.State{PromptID: "p1", Shown: shown("", t0, "sql", "log"),
				Denied: denied("", t0, "a.go"), BashDenied: denied("", t0, "a.go")},
			p:              on,
			wantShown:      shown("", t0, "sql", "log"),
			wantDenied:     denied("", t0, "a.go"),
			wantBashDenied: denied("", t0, "a.go"),
		},
		{
			name:           "a parallel call sooner is denied again, rules already shown",
			in:             BashInput{PromptID: "p1", Files: []BashFile{aGo}, Now: t0.Add(100 * time.Millisecond)},
			st:             session.State{PromptID: "p1", Shown: shown("", t0, "sql", "log"), BashDenied: denied("", t0, "a.go")},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"a.go"}},
			wantShown:      shown("", t0, "sql", "log"),
			wantDenied:     denied("", t0.Add(100*time.Millisecond), "a.go"),
			wantBashDenied: denied("", t0.Add(100*time.Millisecond), "a.go"),
		},
		{
			name:           "a deny in the future (the clock moved back) is no retry",
			in:             BashInput{PromptID: "p1", Files: []BashFile{bGo}, Now: t0},
			st:             session.State{PromptID: "p1", Shown: shown("", t0.Add(time.Hour), "sql"), BashDenied: denied("", t0.Add(time.Hour), "b.go")},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"b.go"}},
			wantShown:      shown("", t0.Add(time.Hour), "sql"),
			wantDenied:     denied("", t0, "b.go"),
			wantBashDenied: denied("", t0, "b.go"),
		},
		{
			name: "a new turn starts over",
			in:   BashInput{PromptID: "p2", Files: []BashFile{bGo}, Now: t0.Add(time.Minute)},
			st: session.State{PromptID: "p1", Shown: shown("", t0, "sql"),
				Denied: denied("", t0, "b.go"), BashDenied: denied("", t0, "b.go")},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"b.go"}, Rules: []Rule{sqlRule}},
			wantShown:      shown("", t0.Add(time.Minute), "sql"),
			wantDenied:     denied("", t0.Add(time.Minute), "b.go"),
			wantBashDenied: denied("", t0.Add(time.Minute), "b.go"),
		},
		{
			name:           "a subagent has its own record",
			in:             BashInput{PromptID: "p1", AgentID: "sub", Files: []BashFile{bGo}, Now: t0.Add(time.Second)},
			st:             session.State{PromptID: "p1", Shown: shown("", t0, "sql"), BashDenied: denied("", t0, "b.go")},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"b.go"}, Rules: []Rule{sqlRule}},
			wantShown:      merge(shown("", t0, "sql"), shown("sub", t0.Add(time.Second), "sql")),
			wantDenied:     denied("sub", t0.Add(time.Second), "b.go"),
			wantBashDenied: merge(denied("", t0, "b.go"), denied("sub", t0.Add(time.Second), "b.go")),
		},
		{
			name:           "files retried go through; a new one is denied alone",
			in:             BashInput{PromptID: "p1", Files: []BashFile{aGo, bGo}, Now: t0.Add(time.Second)},
			st:             session.State{PromptID: "p1", Shown: shown("", t0, "sql", "log"), BashDenied: denied("", t0, "a.go")},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"b.go"}},
			wantShown:      shown("", t0, "sql", "log"),
			wantDenied:     denied("", t0.Add(time.Second), "b.go"),
			wantBashDenied: merge2(denied("", t0, "a.go"), "", "b.go", t0.Add(time.Second)),
		},
		{
			name:           "a rule two files share is shown once",
			in:             BashInput{PromptID: "p1", Files: []BashFile{aGo, bGo}, Now: t0},
			p:              on,
			want:           BashDecision{Deny: true, Files: []string{"a.go", "b.go"}, Rules: []Rule{sqlRule, logRule}},
			wantShown:      shown("", t0, "sql", "log"),
			wantDenied:     denied("", t0, "a.go", "b.go"),
			wantBashDenied: denied("", t0, "a.go", "b.go"),
		},
		{
			name: "backstop off: no deny",
			in:   BashInput{PromptID: "p1", Files: []BashFile{aGo}, Now: t0},
			p:    enforce.Policy{},
		},
		{
			name: "no file: nothing to decide",
			in:   BashInput{PromptID: "p1", Now: t0},
			p:    on,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneState(tt.st)
			d, next := DecideBash(tt.in, tt.st, tt.p)
			if diff := cmp.Diff(tt.want, d, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("decision (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantShown, next.Shown, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("shown (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantDenied, next.Denied, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("denied (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantBashDenied, next.BashDenied, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("bash denied (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, tt.st); diff != "" {
				t.Errorf("DecideBash changed the caller's state (-before +after):\n%s", diff)
			}
		})
	}
}

// A Write to a file whose Bash write was just denied is the agent doing as
// told: the rules it was shown count as read.
func TestDecideBashThenWrite(t *testing.T) {
	_, st := DecideBash(BashInput{PromptID: "p1", Files: []BashFile{{File: "a.go", Rules: []Rule{sqlRule}}}, Now: t0}, session.State{}, on)
	d, _ := Decide(Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlRule}, Now: t0.Add(time.Second)}, st, on)
	if d.Deny {
		t.Errorf("the Write after a Bash deny was denied with %v", d.Rules)
	}
}

// A new turn clears the Bash denies, whichever of Decide and DecideBash
// sees it first.
func TestDecideClearsBashDenied(t *testing.T) {
	st := session.State{PromptID: "p1", BashDenied: denied("", t0, "a.go")}
	_, next := Decide(Input{PromptID: "p2", File: "x.go", Rules: []Rule{logRule}, Now: t0.Add(time.Minute)}, st, on)
	if next.BashDenied != nil {
		t.Errorf("BashDenied after a new turn = %v", next.BashDenied)
	}
	d, _ := DecideBash(BashInput{PromptID: "p2", Files: []BashFile{{File: "a.go", Rules: []Rule{sqlRule}}}, Now: t0.Add(time.Minute + time.Second)}, next, on)
	if !d.Deny {
		t.Error("last turn's Bash deny counted as a retry")
	}
}

func merge(a, b map[string]map[string]time.Time) map[string]map[string]time.Time {
	out := map[string]map[string]time.Time{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// merge2 adds one file for agent to m.
func merge2(m map[string]map[string]time.Time, agent, file string, at time.Time) map[string]map[string]time.Time {
	m[agent][file] = at
	return m
}
