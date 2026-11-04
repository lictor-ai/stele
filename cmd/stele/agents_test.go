package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHookAgentFlag(t *testing.T) {
	if a, ok := hookAgent(nil); !ok || a.Name() != agents[0].Name() {
		t.Errorf("no --agent: %v, %v; want the default agent", a, ok)
	}
	for _, a := range agents {
		got, ok := hookAgent([]string{"--installed=devel", agentFlag + a.Name(), "--check=off"})
		if !ok || got.Name() != a.Name() {
			t.Errorf("--agent=%s: %v, %v", a.Name(), got, ok)
		}
	}
	if _, ok := hookAgent([]string{agentFlag + "no-such-agent"}); ok {
		t.Error("an unknown agent was accepted")
	}
}

// A hook for an agent this stele does not know says nothing: it cannot read
// the payload, and must never stop the agent.
func TestHookUnknownAgentSilent(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	if _, out := hook(t, write(filepath.Join(root, "a.go"), "p1", ""), agentFlag+"no-such-agent"); out != "" {
		t.Errorf("stdout %q, want nothing", out)
	}
	// The run leaves the agent as it found it.
	if agent.Name() != agents[0].Name() {
		t.Errorf("agent after the hook = %s", agent.Name())
	}
}

// With no agent set up, the default one is: its rules file is written before
// stele init, as it always was.
func TestActiveAgentsDefault(t *testing.T) {
	root := t.TempDir()
	got := activeAgents(root)
	if len(got) != 1 || got[0].Name() != agents[0].Name() {
		t.Errorf("activeAgents = %v, want the default agent alone", got)
	}
}
