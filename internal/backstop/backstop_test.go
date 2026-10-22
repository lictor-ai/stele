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

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		in        Input
		st        session.State
		p         enforce.Policy
		wantDeny  bool
		wantRules []Rule
		wantShown map[string]map[string]time.Time
	}{
		{
			name:      "first write of a turn shows the rules",
			in:        Input{PromptID: "p1", Rules: []Rule{sqlRule, logRule}, Now: t0},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule, logRule},
			wantShown: shown("", t0, "sql", "log"),
		},
		{
			name:      "after the window, the retry goes through",
			in:        Input{PromptID: "p1", Rules: []Rule{sqlRule}, Now: t0.Add(10 * time.Second)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantShown: shown("", t0, "sql"),
		},
		{
			name:      "within the window, a parallel write is denied too",
			in:        Input{PromptID: "p1", Rules: []Rule{sqlRule}, Now: t0.Add(time.Second)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule},
			wantShown: shown("", t0, "sql"), // not re-stamped: the window does not slide
		},
		{
			name:      "only the rules not yet shown",
			in:        Input{PromptID: "p1", Rules: []Rule{sqlRule, logRule}, Now: t0.Add(10 * time.Second)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{logRule},
			wantShown: map[string]map[string]time.Time{"": {"sql": t0, "log": t0.Add(10 * time.Second)}},
		},
		{
			name:      "a new turn starts with nothing shown",
			in:        Input{PromptID: "p2", Rules: []Rule{sqlRule}, Now: t0.Add(time.Minute)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule},
			wantShown: shown("", t0.Add(time.Minute), "sql"),
		},
		{
			name:      "a subagent has its own record",
			in:        Input{PromptID: "p1", AgentID: "sub", Rules: []Rule{sqlRule}, Now: t0.Add(time.Minute)},
			st:        session.State{PromptID: "p1", Shown: shown("", t0, "sql")},
			p:         on,
			wantDeny:  true,
			wantRules: []Rule{sqlRule},
			wantShown: map[string]map[string]time.Time{"": {"sql": t0}, "sub": {"sql": t0.Add(time.Minute)}},
		},
		{
			name: "no rules apply",
			in:   Input{PromptID: "p1", Now: t0},
			p:    on,
		},
		{
			name: "backstop off",
			in:   Input{PromptID: "p1", Rules: []Rule{sqlRule}, Now: t0},
			p:    enforce.Policy{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.st
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
			if diff := cmp.Diff(before, tt.st); diff != "" {
				t.Errorf("Decide changed the caller's state (-before +after):\n%s", diff)
			}
		})
	}
}
