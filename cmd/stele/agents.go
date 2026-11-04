package main

import (
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/adapter/codex"
)

// agents are the agents stele knows, the default first. This is the only
// file in cmd/stele that names one (ADR-0009).
var agents = []adapter.Adapter{claudecode.Adapter{}, codex.Adapter{}}

// agent is the agent this run answers: stele hook sets it from the command's
// --agent, which init writes (ADR-0012); every other command works for each
// agent in turn.
var agent adapter.Adapter = agents[0]

// agentFlag names, on the hook command init installs, the agent it answers.
// With none, the hook answers the default agent, as every hook installed
// before there was a second one does.
const agentFlag = "--agent="

// agentNamed returns the agent whose Name is name.
func agentNamed(name string) (adapter.Adapter, bool) {
	for _, a := range agents {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}

// hookAgent returns the agent stele hook's own arguments name, the default
// when none does. ok is false for a name stele does not know: such a hook
// says nothing, as it does for any input it cannot read.
func hookAgent(args []string) (a adapter.Adapter, ok bool) {
	a = agents[0]
	for _, arg := range args {
		if name, found := strings.CutPrefix(arg, agentFlag); found {
			if a, ok = agentNamed(name); !ok {
				return nil, false
			}
		}
	}
	return a, true
}
