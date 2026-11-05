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

// Many files' messages stay within the cap, together: what does not fit is
// counted, never dropped silently.
func TestAfterWritesCapsTheWholeAnswer(t *testing.T) {
	root := project(t, checkRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	body := "package x\n\n" + strings.Repeat("// TODO later\nvar password = 1\n", 30)
	var edits []adapter.Edit
	for i := range 12 {
		f := filepath.Join(root, string(rune('a'+i))+".go")
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		edits = append(edits, adapter.Edit{File: f, Writes: true, Whole: true})
	}
	a := afterWrites(adapter.Event{Kind: adapter.PostTool, SessionID: "s1", Cwd: root, Edits: edits}, adapter.Switches{})
	if !a.Deny || len(a.Reason) > install.MaxMessage || len(a.Context) > install.MaxMessage {
		t.Fatalf("reason %d bytes, context %d bytes, want each within %d", len(a.Reason), len(a.Context), install.MaxMessage)
	}
	if !strings.Contains(a.Context, "more of the files this call wrote") {
		t.Errorf("the files left unchecked are not counted:\n%s", a.Context)
	}
}

// A project's note, here a stele.md that stopped parsing, is given once,
// however many of its files the call wrote.
func TestAfterWritesNoteOnce(t *testing.T) {
	root := project(t, checkRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	var edits []adapter.Edit
	for _, rel := range []string{"a.go", "b.go", "c.go"} {
		f := filepath.Join(root, rel)
		if err := os.WriteFile(f, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		edits = append(edits, adapter.Edit{File: f, Writes: true, Whole: true})
	}
	ev := adapter.Event{Kind: adapter.PostTool, SessionID: "s1", Cwd: root, Edits: edits}
	afterWrites(ev, adapter.Switches{}) // builds the index
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte("scope: [\"a\n\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := afterWrites(ev, adapter.Switches{})
	if n := strings.Count(a.Context, "The last valid rules still apply"); n != 1 {
		t.Errorf("the note is given %d times, want once:\n%s", n, a.Context)
	}
}

// Every project a call denies in shows at least one of its rules, however
// long the first project's.
func TestBeforeWriteEveryRootShowsARule(t *testing.T) {
	long := `scope: ["**/*.go"]` + "\n\n" + strings.Repeat("Keep it long. ", 700) + "\n"
	root := project(t, long)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "stele.md"), []byte("The sub rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := adapter.Event{Kind: adapter.PreTool, SessionID: "s1", PromptID: "p1", Cwd: root, Edits: []adapter.Edit{
		{File: filepath.Join(root, "a.go"), Writes: true},
		{File: filepath.Join(sub, "b.go"), Writes: true},
	}}
	a := beforeWrite(ev, adapter.Switches{})
	if !a.Deny || !strings.Contains(a.Reason, "Keep it long.") || !strings.Contains(a.Reason, "The sub rule.") {
		t.Errorf("reason does not show a rule of each project:\n%.300s…", a.Reason)
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
