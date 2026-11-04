package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/install"
)

// One tool call may touch several files, as a patch does (ADR-0012). These
// call the neutral flow directly: the Claude Code adapter gives one Edit.

func TestBeforeWriteSeveralFilesOneDeny(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	ev := adapter.Event{Kind: adapter.PreTool, SessionID: "s1", PromptID: "p1", Cwd: root, Edits: []adapter.Edit{
		{File: filepath.Join(root, "a.go"), Writes: true, Whole: true},
		{File: filepath.Join(root, "README.md"), Writes: true},
	}}
	a := beforeWrite(ev, adapter.Switches{})
	if !a.Deny {
		t.Fatalf("answer %+v, want a deny", a)
	}
	for _, want := range []string{"a.go, README.md", "Never build SQL", "Log through the shared logger"} {
		if !strings.Contains(a.Reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, a.Reason)
		}
	}
	if n := strings.Count(a.Reason, "Log through the shared logger"); n != 1 {
		t.Errorf("a rule for both files shown %d times, want once", n)
	}
	// The same patch again, once the agent has read the reason, is its retry.
	advance(2 * time.Second)
	if a := beforeWrite(ev, adapter.Switches{}); a.Deny {
		t.Errorf("the retry of the same patch was denied: %+v", a)
	}
}

func TestBeforeWriteRemovalShowsNoRules(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	ev := adapter.Event{Kind: adapter.PreTool, SessionID: "s1", PromptID: "p1", Cwd: root,
		Edits: []adapter.Edit{{File: filepath.Join(root, "a.go"), Removes: true}}}
	if a := beforeWrite(ev, adapter.Switches{}); a.Deny || a.Ask != "" {
		t.Errorf("removing a.go: %+v, want nothing", a)
	}
}

func TestBeforeWriteAsksForOwnFileInAPatch(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	ev := adapter.Event{Kind: adapter.PreTool, SessionID: "s1", PromptID: "p1", Cwd: root, Edits: []adapter.Edit{
		{File: filepath.Join(root, "a.go"), Writes: true},
		{File: filepath.Join(root, "stele.md"), Removes: true},
	}}
	a := beforeWrite(ev, adapter.Switches{})
	if a.Ask == "" || !strings.Contains(a.Ask, "stele.md") || a.Deny {
		t.Errorf("a patch deleting stele.md: %+v, want an ask naming it", a)
	}
}

func TestAfterWritesChecksEveryFile(t *testing.T) {
	root := project(t, checkRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	var edits []adapter.Edit
	for _, rel := range []string{"a.go", "b.go"} {
		f := filepath.Join(root, rel)
		if err := os.WriteFile(f, []byte("package x\n\nvar password = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		edits = append(edits, adapter.Edit{File: f, Writes: true, Whole: true})
	}
	edits = append(edits, adapter.Edit{File: filepath.Join(root, "gone.go"), Removes: true})
	a := afterWrites(adapter.Event{Kind: adapter.PostTool, SessionID: "s1", Cwd: root, Edits: edits}, adapter.Switches{})
	if !a.Deny {
		t.Fatalf("answer %+v, want a block", a)
	}
	for _, want := range []string{"a.go:3:5:", "b.go:3:5:"} {
		if !strings.Contains(a.Reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, a.Reason)
		}
	}
	if len(a.Reason) > install.MaxMessage {
		t.Errorf("reason is %d bytes, over the %d cap", len(a.Reason), install.MaxMessage)
	}
}
