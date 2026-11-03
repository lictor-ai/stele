package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/journal"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/safefile"
)

// logLine is a line of .stele/log.jsonl, every kind's fields together.
type logLine struct {
	V          int                 `json:"v"`
	Kind       journal.Kind        `json:"kind"`
	Project    string              `json:"project"`
	TS         time.Time           `json:"ts"`
	Session    string              `json:"session"`
	Agent      string              `json:"agent"`
	Subagent   string              `json:"subagent"`
	Prompt     string              `json:"prompt"`
	Via        string              `json:"via"`
	File       string              `json:"file"`
	Files      []string            `json:"files"`
	Rules      []string            `json:"rules"`
	More       int                 `json:"more"`
	Violations []journal.Violation `json:"violations"`
	Mode       string              `json:"mode"`
	Errors     int                 `json:"errors"`
	Exit       int                 `json:"exit"`
	Broken     map[string]int      `json:"broken"`
}

// readLog returns the lines of root's log this machine signed, and fails the
// test on any it did not.
func readLog(t *testing.T, root string) []logLine {
	t.Helper()
	key, err := loadKey()
	if err != nil {
		t.Fatal(err)
	}
	var out []logLine
	bad, err := journal.Read(root, key, func(_ journal.Kind, body []byte) error {
		var l logLine
		if err := json.Unmarshal(body, &l); err != nil {
			return err
		}
		out = append(out, l)
		return nil
	})
	if err != nil || bad != 0 {
		t.Fatalf("reading the log: %d bad lines, %v", bad, err)
	}
	return out
}

func kinds(ls []logLine) string {
	var ks []string
	for _, l := range ls {
		ks = append(ks, string(l.Kind))
	}
	return strings.Join(ks, " ")
}

// projectID is the project id stele.lock holds.
func projectID(t *testing.T, root string) string {
	t.Helper()
	lf, err := lock.Load(root)
	if err != nil || lf.Project == "" {
		t.Fatalf("stele.lock: %+v, %v", lf, err)
	}
	return lf.Project
}

// A write denied to show rules is logged with the rules it showed.
func TestHookLogsDeny(t *testing.T) {
	root := project(t, hookRules)
	start := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	clock(t, start)
	if o, _ := hook(t, write(filepath.Join(root, "internal", "q.go"), "p1", "sub1")); !denied(o) {
		t.Fatal("the write was not denied")
	}
	ls := readLog(t, root)
	if len(ls) != 1 {
		t.Fatalf("log = %s, want one denied line", kinds(ls))
	}
	l := ls[0]
	if l.V != 1 || l.Kind != journal.Denied || l.Project != projectID(t, root) || !l.TS.Equal(start) ||
		l.Session != "s1" || l.Agent != agent.Name() || l.Subagent != "sub1" || l.Prompt != "p1" || l.Via != "write" {
		t.Errorf("line = %+v", l)
	}
	if strings.Join(l.Files, ",") != "internal/q.go" || len(l.Rules) != 2 || l.More != 0 {
		t.Errorf("files %v, rules %v, more %d; want internal/q.go and both rules", l.Files, l.Rules, l.More)
	}
}

// Before a write, only a deny is logged: a write let through is logged by
// the post-write call, once it happened (D70).
func TestHookLogsNoAllow(t *testing.T) {
	root := project(t, hookRules)
	advance := clock(t, time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
	goFile := filepath.Join(root, "a.go")
	hook(t, write(goFile, "p1", ""))
	advance(2 * time.Second)
	if o, _ := hook(t, write(goFile, "p1", "")); denied(o) {
		t.Fatal("the retry was denied")
	}
	if ls := readLog(t, root); kinds(ls) != "denied" {
		t.Errorf("log = %q, want only the deny", kinds(ls))
	}
}

// A Bash command denied is logged with every file it was denied for.
func TestHookLogsBashDeny(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	data, err := os.ReadFile(filepath.Join(testdata, "hook", "pre-bash.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p["cwd"] = root
	if o, _ := hook(t, p); !denied(o) {
		t.Fatal("the recorded Bash payload was not denied")
	}
	ls := readLog(t, root)
	if len(ls) != 1 || ls[0].Kind != journal.Denied || ls[0].Via != "shell" {
		t.Fatalf("log = %+v, want one shell deny", ls)
	}
	if strings.Join(ls[0].Files, ",") != "internal/db/query.go" || len(ls[0].Rules) == 0 {
		t.Errorf("files %v, rules %v", ls[0].Files, ls[0].Rules)
	}
}

// A check after a write is logged with the rule, file and line it found.
func TestHookLogsCheckedViolation(t *testing.T) {
	root := project(t, checkRules)
	if o, _ := postHook(t, root, "post-write", "internal/db.go", "package db\n\nvar password = 1\n"); o.Decision != "block" {
		t.Fatalf("decision %q, want block", o.Decision)
	}
	ls := readLog(t, root)
	if kinds(ls) != "wrote checked" {
		t.Fatalf("log = %s, want the write, then its check", kinds(ls))
	}
	l := ls[1]
	if l.File != "internal/db.go" || l.Session != "abc123" || l.Agent != agent.Name() || len(l.Rules) == 0 {
		t.Errorf("line = %+v", l)
	}
	if len(l.Violations) != 1 || l.Violations[0].Line != 3 || !l.Violations[0].Blocking || l.Violations[0].Rule == "" {
		t.Errorf("violations = %+v, want one blocking on line 3", l.Violations)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".stele", journal.FileName))
	if strings.Contains(string(data), "password") {
		t.Errorf("the log holds code or rule text:\n%s", data)
	}
}

// A clean check is logged too: stele stats tells a fixed rule from one
// never checked again.
func TestHookLogsCheckedClean(t *testing.T) {
	root := project(t, checkRules)
	if _, out := postHook(t, root, "post-write", "internal/db.go", "package db\n"); out != "" {
		t.Fatalf("stdout %q, want nothing", out)
	}
	ls := readLog(t, root)
	if kinds(ls) != "wrote checked" || ls[1].Violations == nil || len(ls[1].Violations) != 0 {
		t.Fatalf("log = %+v, want the write and one clean checked line", ls)
	}
	if len(ls[1].Rules) != 2 {
		t.Errorf("rules = %v, want both checked rules on a .go file", ls[1].Rules)
	}
	// A README is checked against the unscoped rule only.
	postHook(t, root, "post-write", "README.md", "hello\n")
	if ls := readLog(t, root); len(ls) != 4 || len(ls[3].Rules) != 1 {
		t.Errorf("log = %+v, want a second check with one rule", ls)
	}
}

// A binary file is skipped by the checker, so no rule was checked: no line
// says it was checked clean.
func TestHookLogsNoBinaryCheck(t *testing.T) {
	root := project(t, checkRules)
	postHook(t, root, "post-write", "internal/db.go", "package db\x00\nvar password = 1\n")
	if ls := readLog(t, root); kinds(ls) != "wrote" {
		t.Errorf("log = %s, want only the write for a skipped binary file", kinds(ls))
	}
}

// A write no checked rule covers is logged as written, with no check.
func TestHookLogsNoCheck(t *testing.T) {
	root := project(t, hookRules)
	postHook(t, root, "post-write", "a.go", "package a\n")
	if ls := readLog(t, root); kinds(ls) != "wrote" {
		t.Errorf("log = %s, want only the write: no rule has a check", kinds(ls))
	}
}

// A write is logged with every rule that applies to the file, checked or
// not, so stele stats can tell a rule no write ever met (D70).
func TestHookLogsWrote(t *testing.T) {
	root := project(t, hookRules)
	postHook(t, root, "post-write", "internal/q.go", "package q\n")
	postHook(t, root, "post-write", "notes.txt", "hi\n")
	ls := readLog(t, root)
	if kinds(ls) != "wrote wrote" {
		t.Fatalf("log = %s, want two writes", kinds(ls))
	}
	l := ls[0]
	if l.File != "internal/q.go" || l.Session != "abc123" || l.Agent != agent.Name() || l.Project != projectID(t, root) || len(l.Rules) != 2 {
		t.Errorf("line = %+v, want internal/q.go with both rules", l)
	}
	// Only the unscoped rule applies to a text file.
	if ls[1].File != "notes.txt" || len(ls[1].Rules) != 1 {
		t.Errorf("line = %+v, want notes.txt with the unscoped rule", ls[1])
	}
}

// The rules a session loaded at its start are logged.
func TestHookLogsLoaded(t *testing.T) {
	root := primedProject(t)
	startSession(t, "SessionStart", root, "", "startup")
	ls := readLog(t, root)
	if len(ls) != 1 || ls[0].Kind != journal.Loaded || ls[0].Session != "s1" || ls[0].Subagent != "" {
		t.Fatalf("log = %+v, want one loaded line", ls)
	}
	if len(ls[0].Rules) != 2 || ls[0].Project != projectID(t, root) {
		t.Errorf("line = %+v, want both rules", ls[0])
	}
	startSubagent(t, root, "sub2", "general-purpose")
	ls = readLog(t, root)
	if len(ls) != 2 || ls[1].Subagent != "sub2" || len(ls[1].Rules) != 2 {
		t.Errorf("after a subagent: %+v", ls)
	}
}

// A log stele cannot write changes nothing in the answer.
func TestHookLogFailureChangesNothing(t *testing.T) {
	answer := func(t *testing.T, spoil func(root string)) string {
		root := project(t, hookRules)
		clock(t, time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile = %d: %s", code, errOut)
		}
		spoil(root)
		_, out := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
		return strings.ReplaceAll(out, filepath.ToSlash(root), "ROOT")
	}
	want := answer(t, func(string) {})
	t.Run("a directory where the log goes", func(t *testing.T) {
		got := answer(t, func(root string) {
			if err := os.Mkdir(filepath.Join(root, ".stele", journal.FileName), 0o755); err != nil {
				t.Fatal(err)
			}
		})
		if got != want {
			t.Errorf("answer = %s\nwant %s", got, want)
		}
	})
	t.Run("the log's lock held", func(t *testing.T) {
		got := answer(t, func(root string) {
			unlock, err := safefile.Lock(filepath.Join(root, ".stele", journal.FileName+".lock"), time.Second, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unlock)
		})
		if got != want {
			t.Errorf("answer = %s\nwant %s", got, want)
		}
	})
}

// Outside a stele project nothing is logged, and no .stele is made.
func TestHookLogsNothingOutsideStele(t *testing.T) {
	root := project(t, "")
	hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	hook(t, sessionPayload(t, "SessionStart", root))
	if _, err := os.Lstat(filepath.Join(root, ".stele")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a project without stele got .stele: %v", err)
	}
}

// With --check=off nothing is checked, so only the write is logged.
func TestHookLogSwitchesOff(t *testing.T) {
	root := project(t, checkRules)
	p := postPayload(t, root, "post-write", "internal/db.go", "package db\n\nvar password = 1\n")
	hook(t, p, checkOff...)
	if ls := readLog(t, root); kinds(ls) != "wrote" || len(ls[0].Rules) != 3 {
		t.Errorf("log = %+v with --check=off, want only the write, with its rules", ls)
	}
}
