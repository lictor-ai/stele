package backstop

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/session"
)

var (
	sqlHashed = Rule{ID: "sql", Text: sqlRule.Text, Hash: "h-sql"}
	logHashed = Rule{ID: "log", Text: logRule.Text, Hash: "h-log"}
)

// primed is the state of agents that loaded these rule hashes at start.
func primed(agent string, rs ...Rule) map[string]map[string]string {
	m := map[string]string{}
	for _, r := range rs {
		m[r.ID] = r.Hash
	}
	return map[string]map[string]string{agent: m}
}

// A rule the agent loaded from the rules file, unchanged since, counts as
// seen for the whole session (#68).
func TestDecidePrimed(t *testing.T) {
	changed := Rule{ID: "sql", Text: "Never build SQL by hand.", Hash: "h-sql-2"}
	unhashed := Rule{ID: "sql", Text: sqlRule.Text}
	tests := []struct {
		name      string
		in        Input
		st        session.State
		wantDeny  bool
		wantRules []Rule
	}{
		{
			name: "primed and unchanged: no deny",
			in:   Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlHashed, logHashed}, Now: t0},
			st:   session.State{Primed: primed("", sqlHashed, logHashed)},
		},
		{
			name:      "primed but changed since: denied with that rule",
			in:        Input{PromptID: "p1", File: "a.go", Rules: []Rule{changed, logHashed}, Now: t0},
			st:        session.State{Primed: primed("", sqlHashed, logHashed)},
			wantDeny:  true,
			wantRules: []Rule{changed},
		},
		{
			name:      "a rule added since: denied with it",
			in:        Input{PromptID: "p1", File: "a.go", Rules: []Rule{sqlHashed, logHashed}, Now: t0},
			st:        session.State{Primed: primed("", sqlHashed)},
			wantDeny:  true,
			wantRules: []Rule{logHashed},
		},
		{
			name:      "an empty hash never matches",
			in:        Input{PromptID: "p1", File: "a.go", Rules: []Rule{unhashed}, Now: t0},
			st:        session.State{Primed: map[string]map[string]string{"": {"sql": ""}}},
			wantDeny:  true,
			wantRules: []Rule{unhashed},
		},
		{
			name: "a new turn keeps what was primed",
			in:   Input{PromptID: "p2", File: "a.go", Rules: []Rule{sqlHashed}, Now: t0},
			st:   session.State{PromptID: "p1", Primed: primed("", sqlHashed)},
		},
		{
			name: "a subagent with its own primed rules",
			in:   Input{PromptID: "p1", AgentID: "sub", File: "a.go", Rules: []Rule{sqlHashed}, Now: t0},
			st:   session.State{Primed: primed("sub", sqlHashed)},
		},
		{
			name:      "a subagent does not use the main agent's",
			in:        Input{PromptID: "p1", AgentID: "sub", File: "a.go", Rules: []Rule{sqlHashed}, Now: t0},
			st:        session.State{Primed: primed("", sqlHashed)},
			wantDeny:  true,
			wantRules: []Rule{sqlHashed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, st := Decide(tt.in, tt.st)
			if d.Deny != tt.wantDeny {
				t.Fatalf("deny = %v, want %v", d.Deny, tt.wantDeny)
			}
			if diff := cmp.Diff(tt.wantRules, d.Rules); diff != "" {
				t.Errorf("rules (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.st.Primed, st.Primed); diff != "" {
				t.Errorf("Decide changed Primed (-before +after):\n%s", diff)
			}
		})
	}
}

// A Bash command writing a file with rules is still stopped once when every
// rule is primed: the write would go unchecked. No rule is listed.
func TestDecideBashPrimed(t *testing.T) {
	in := BashInput{PromptID: "p1", Files: []BashFile{{File: "a.go", Rules: []Rule{sqlHashed, logHashed}}}, Now: t0}
	d, st := DecideBash(in, session.State{Primed: primed("", sqlHashed, logHashed)})
	if !d.Deny || len(d.Rules) != 0 || !cmp.Equal(d.Files, []string{"a.go"}) {
		t.Errorf("decision = %+v, want a deny of a.go listing no rule", d)
	}
	if st.Shown[""] != nil {
		t.Errorf("shown = %v, want nothing newly shown", st.Shown)
	}

	// A rule changed since is listed.
	changed := Rule{ID: "log", Text: "Log with slog.", Hash: "h-log-2"}
	in.Files[0].Rules = []Rule{sqlHashed, changed}
	d, _ = DecideBash(in, session.State{Primed: primed("", sqlHashed, logHashed)})
	if !cmp.Equal(d.Rules, []Rule{changed}) {
		t.Errorf("rules = %+v, want only the changed one", d.Rules)
	}
}
