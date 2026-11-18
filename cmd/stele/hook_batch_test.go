package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
)

// batchWrite is a PreTool event writing file in root, in turn p1.
func batchWrite(root, file string) adapter.Event {
	return adapter.Event{Kind: adapter.PreTool, SessionID: "s1", PromptID: "p1", Cwd: root, Edits: []adapter.Edit{
		{File: filepath.Join(root, file), Writes: true, Whole: true},
	}}
}

// A hook of a parallel batch can wait seconds for the others' lock on a
// loaded machine (#151). It is in the batch because it started before the
// deny before it was answered, whenever it decides.
func TestBeforeWriteBatchByStart(t *testing.T) {
	root := project(t, hookRules)
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	advance := clock(t, t0)
	if a := beforeWrite(batchWrite(root, "a.go"), adapter.Switches{}); !a.Deny {
		t.Fatalf("first write: %+v, want a deny", a)
	}

	// b.go's hook started 100 ms in, and decides 10 s in.
	t.Cleanup(func() { started = time.Time{} })
	started = t0.Add(100 * time.Millisecond)
	advance(10 * time.Second)
	a := beforeWrite(batchWrite(root, "b.go"), adapter.Switches{})
	if !a.Deny || !strings.Contains(a.Reason, "Never build SQL") {
		t.Fatalf("a parallel write that waited 10 s for the lock: %+v, want a deny with the rules", a)
	}
	if !strings.Contains(a.Reason, "not read yet") {
		t.Errorf("the reason does not say the rules are not read yet:\n%s", a.Reason)
	}

	// The agent reads the reasons, and retries both.
	started = time.Time{}
	advance(2 * time.Second)
	for _, f := range []string{"a.go", "b.go"} {
		if a := beforeWrite(batchWrite(root, f), adapter.Switches{}); a.Deny {
			t.Errorf("retry of %s denied: %+v", f, a)
		}
	}
	// So does a write to a file it has not written: the rules are read.
	advance(5 * time.Second)
	if a := beforeWrite(batchWrite(root, "c.go"), adapter.Switches{}); a.Deny {
		t.Errorf("a later write was shown rules already read: %+v", a)
	}
}

// A test build's window (parseWindow) widens or narrows the batch window;
// 0 leaves only the chain.
func TestBeforeWriteTestWindow(t *testing.T) {
	for _, tt := range []struct {
		env      string
		wantDeny bool
	}{
		{"", false},      // the real 3 s window: 10 s later is outside it
		{"1m", true},     // widened
		{"0", false},     // the chain only
		{"junk", false},  // ignored
		{"2h", false},    // past a minute: ignored
		{"-1s", false},   // ignored
		{"30s", true},    // widened
		{"500ms", false}, // narrowed
	} {
		t.Run(tt.env, func(t *testing.T) {
			old := batchWindow
			batchWindow = func() time.Duration { return parseWindow(tt.env) }
			t.Cleanup(func() { batchWindow = old })
			root := project(t, hookRules)
			advance := clock(t, time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC))
			if a := beforeWrite(batchWrite(root, "a.go"), adapter.Switches{}); !a.Deny {
				t.Fatalf("first write: %+v, want a deny", a)
			}
			advance(10 * time.Second)
			if a := beforeWrite(batchWrite(root, "b.go"), adapter.Switches{}); a.Deny != tt.wantDeny {
				t.Errorf("another file 10 s later: deny %v, want %v", a.Deny, tt.wantDeny)
			}
		})
	}
}

// The hook waits for the session lock only as long as is left of its time:
// one that already spent 8.4 s, in compile.EnsureFresh say, gives up at once
// and warns, before the agent kills it with no word (#151).
func TestBeforeWriteSessionWaitWithinBudget(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC))
	if a := beforeWrite(batchWrite(root, "a.go"), adapter.Switches{}); !a.Deny {
		t.Fatalf("first write: %+v, want a deny", a)
	}
	// Another hook holds the session's lock.
	lock := filepath.Join(root, ".stele", "state", "s1.json.lock")
	if err := os.WriteFile(lock, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := sessionWait(); d != sessionWaitMax {
		t.Errorf("outside a hook call, sessionWait = %v, want %v", d, sessionWaitMax)
	}
	t.Cleanup(func() { startedWall = time.Time{} })
	startedWall = time.Now().Add(-8400 * time.Millisecond)
	if d := sessionWait(); d > 100*time.Millisecond {
		t.Errorf("8.4 s in, sessionWait = %v, want at most 100 ms", d)
	}
	start := time.Now()
	a := beforeWrite(batchWrite(root, "b.go"), adapter.Switches{})
	if d := time.Since(start); d >= 3*time.Second {
		t.Errorf("waited %v for the lock, past the hook's time", d)
	}
	if a.Deny || !strings.Contains(a.Warning, "rules not enforced") {
		t.Errorf("a hook out of time: %+v, want the write through with a warning", a)
	}
}

// A Bash deny lists the rules the agent has not read: a parallel call in the
// batch lists them again, and a call after it, when the agent has them all,
// says so. It never claims rules and lists none (#151).
func TestBashDenyListsRulesNotRead(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC))
	bash := func(cmd string) adapter.Answer {
		t.Helper()
		ev := adapter.Event{Kind: adapter.PreShell, SessionID: "s1", PromptID: "p1", Cwd: root, Command: cmd}
		a := beforeBash(ev, adapter.Switches{})
		if !a.Deny {
			t.Fatalf("%s: %+v, want a deny", cmd, a)
		}
		return a
	}
	if a := bash("echo x > a.go"); !strings.Contains(a.Reason, "Never build SQL") {
		t.Errorf("first call lacks the rules:\n%s", a.Reason)
	}
	advance(100 * time.Millisecond)
	if a := bash("echo x > b.go"); !strings.Contains(a.Reason, "Never build SQL") || strings.Contains(a.Reason, "already been given") {
		t.Errorf("a parallel call, the rules not read yet:\n%s", a.Reason)
	}
	advance(time.Minute)
	if a := bash("echo x > c.go"); !strings.Contains(a.Reason, "already been given its rules") || strings.Contains(a.Reason, "Never build SQL") {
		t.Errorf("a call after the batch, every rule read:\n%s", a.Reason)
	}
}
