package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In CI, with no key and no .stele/, check --push sends one record of the
// run's counts, with no MAC, and never the report (D84).
func TestCheckPushCIOneRecord(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "good")
	root := gitProject(t, checkRules)
	out := filepath.Join(t.TempDir(), "received.jsonl")
	withSink(t, root, "good", out, "")
	if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
		t.Fatal(err)
	}
	ci := t.TempDir()
	old := keyDir
	keyDir = func() (string, error) { return ci, nil }
	t.Cleanup(func() { keyDir = old })

	writeAt(t, root, "db.go", "package db\n\nvar password = \"hunter2\"\n")
	code, stdout, errOut := runCmd("check", "--push", "db.go")
	if code != exitFailed {
		t.Fatalf("check --push = %d, want %d (the check's); stderr %q", code, exitFailed, errOut)
	}
	if !strings.Contains(stdout, "good: sent 1 line") {
		t.Errorf("stdout = %q", stdout)
	}
	got := received(t, out)
	if len(got) != 1 {
		t.Fatalf("the sink got %d records: %v", len(got), got)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(got[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["kind"] != "check" || rec["v"] != float64(1) || rec["errors"] != float64(1) || rec["project"] != projectID(t, root) {
		t.Errorf("record = %v", rec)
	}
	if _, ok := rec["mac"]; ok {
		t.Error("a CI record has a mac")
	}
	if strings.Contains(got[0], "hunter2") || strings.Contains(got[0], "password") {
		t.Errorf("the record holds code or rule text: %s", got[0])
	}
	if entries, _ := os.ReadDir(ci); len(entries) != 0 {
		t.Errorf("check --push made a key: %v", entries)
	}
}

// Where stele runs, check --push pushes the log's pending lines, this run's
// included.
func TestCheckPushDevMachinePushesPending(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "good")
	root := gitProject(t, checkRules)
	out := filepath.Join(t.TempDir(), "received.jsonl")
	withSink(t, root, "good", out, "")
	code, stdout, errOut := runCmd("check", "--push", "--format", "json")
	if code != exitOK {
		t.Fatalf("check --push = %d: %s", code, errOut)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not one report: %v\n%s", err, stdout)
	}
	if !strings.Contains(errOut, "good: sent") {
		t.Errorf("stderr = %q, want what was sent", errOut)
	}
	got := received(t, out)
	if len(got) == 0 || !strings.Contains(got[len(got)-1], `"kind":"check"`) || !strings.Contains(got[len(got)-1], `"mac":"`) {
		t.Errorf("the sink got %v, want the log ending with this run's signed check line", got)
	}
	logData, _ := os.ReadFile(filepath.Join(root, ".stele", "log.jsonl"))
	if strings.Join(got, "\n")+"\n" != string(logData) {
		t.Errorf("the sink got\n%s\nwant the log\n%s", strings.Join(got, "\n"), logData)
	}
}

func TestCheckPushFailureIsExit2(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "errors")
	root := gitProject(t, checkRules)
	withSink(t, root, "errors", filepath.Join(t.TempDir(), "x"), "")
	if code, _, errOut := runCmd("check", "--push"); code != exitCantRun || !strings.Contains(errOut, "stele-sink-errors push") {
		t.Errorf("check --push with a failing sink = %d, %q", code, errOut)
	}
	withSink(t, root, "missing", filepath.Join(t.TempDir(), "x"), "")
	code, stdout, errOut := runCmd("check", "--push")
	if code != exitCantRun || !strings.Contains(errOut, "stele-sink-missing not found") || strings.Contains(stdout, "checked") {
		t.Errorf("check --push with a missing sink = %d, %q, %q: want exit 2 before the check", code, stdout, errOut)
	}
}

func TestCheckPushNoSink(t *testing.T) {
	gitProject(t, checkRules)
	code, stdout, _ := runCmd("check", "--push")
	if code != exitOK || !strings.Contains(stdout, "no sink in stele.json") {
		t.Errorf("check --push = %d, %q", code, stdout)
	}
}
