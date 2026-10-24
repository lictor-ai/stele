package main

import (
	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/install"
)

// startsSession reports whether event starts a session or a subagent: hook
// calls that name no file.
func startsSession(event string) bool {
	return event == "SessionStart" || event == "SubagentStart"
}

// sessionStart tells the agent, before its first prompt, to write files with
// Write and Edit only. Agents in tests wrote whole files through Bash, which
// stele never sees; the deny message alone comes too late when that first
// write is a Bash one (#50). It reads nothing but the project's root, and
// says nothing outside a stele project.
func sessionStart(ev claudecode.Event) claudecode.Answer {
	if ev.Cwd == "" {
		return claudecode.Answer{}
	}
	root, err := findRoot(ev.Cwd)
	if err != nil || !usesStele(root) {
		return claudecode.Answer{}
	}
	return claudecode.Answer{Context: install.SessionContext()}
}
