package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
)

// A PowerShell command, Codex's shell on Windows, is read as PowerShell
// (D77): Set-Content writing a file a scoped rule covers is stopped, and one
// writing stele's own files asks, whatever OS the test runs on.
func TestBeforeBashPowerShell(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	ev := adapter.Event{Kind: adapter.PreShell, SessionID: "s1", PromptID: "p1", Cwd: root, Dialect: adapter.PowerShell,
		Command: "Set-Content -Path internal/db.go -Value 'package db'"}
	a := beforeBash(ev, adapter.Switches{})
	if !a.Deny || !strings.Contains(a.Reason, "internal/db.go") || !strings.Contains(a.Reason, "Never build SQL") {
		t.Errorf("Set-Content into a scoped file: %+v", a)
	}
	ev.Command = "Remove-Item stele.md"
	if a := beforeBash(ev, adapter.Switches{}); a.Ask == "" || !strings.Contains(a.Ask, "stele.md") {
		t.Errorf("Remove-Item stele.md: %+v", a)
	}
	// Read as bash, Set-Content writes nothing.
	ev.Dialect, ev.Command, ev.PromptID = adapter.Bash, "Set-Content -Path internal/other.go -Value x", "p2"
	if a := beforeBash(ev, adapter.Switches{}); a.Deny {
		t.Errorf("bash read a PowerShell write: %+v", a)
	}
}
