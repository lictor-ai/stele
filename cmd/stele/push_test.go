package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/journal"
)

// withSink lists the sink name in stele.json, writing what it accepts to out.
func withSink(t *testing.T, root, name, out, extra string) {
	t.Helper()
	writeAt(t, root, "stele.json", `{"sinks": [{"name": "`+name+`", "config": {"out": `+jsonString(out)+extra+`}}]}`+"\n")
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func received(t *testing.T, out string) []string {
	t.Helper()
	data, err := os.ReadFile(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// pushProject is a compiled project with three wrote lines in its log.
func pushProject(t *testing.T) string {
	t.Helper()
	root := compiled(t, rules)
	for _, s := range []string{"s1", "s2", "s3"} {
		logLines(t, root, &journal.WroteRecord{Header: statsHeader(t, root, "claude-code", s, statsNow), File: "a.go"})
	}
	return root
}

func TestPushNoSinksSendsNothing(t *testing.T) {
	pushProject(t)
	code, out, errOut := runCmd("push")
	if code != exitOK || !strings.Contains(out, "no sink in stele.json") {
		t.Errorf("push = %d, %q, %q", code, out, errOut)
	}
}

func TestPushEndToEndFakeSink(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "good")
	root := pushProject(t)
	out := filepath.Join(t.TempDir(), "received.jsonl")
	withSink(t, root, "good", out, "")
	code, stdout, errOut := runCmd("push")
	if code != exitOK || !strings.Contains(stdout, "good: sent 3 lines") {
		t.Fatalf("push = %d, %q, %q", code, stdout, errOut)
	}
	logData, _ := os.ReadFile(filepath.Join(root, ".stele", journal.FileName))
	if got := strings.Join(received(t, out), "\n") + "\n"; got != string(logData) {
		t.Errorf("the sink got\n%s\nwant the log as written\n%s", got, logData)
	}
	code, stdout, _ = runCmd("push")
	if code != exitOK || !strings.Contains(stdout, "good: sent 0 lines") || len(received(t, out)) != 3 {
		t.Errorf("second push = %d, %q, %d received", code, stdout, len(received(t, out)))
	}
	st := statusJSON(t)
	if len(st.Sinks) != 1 || st.Sinks[0].Pending != 0 || st.Sinks[0].LastPush == "" {
		t.Errorf("status sinks = %+v", st.Sinks)
	}
}

func TestPushPartialIsExit1(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "good")
	root := pushProject(t)
	out := filepath.Join(t.TempDir(), "received.jsonl")
	withSink(t, root, "good", out, `, "accept": 2`)
	code, stdout, _ := runCmd("push")
	if code != exitFailed || !strings.Contains(stdout, "1 line wait") {
		t.Errorf("partial push = %d, %q", code, stdout)
	}
	if st := statusJSON(t); st.Sinks[0].Pending != 1 {
		t.Errorf("pending = %+v", st.Sinks)
	}
}

func TestPushExitCodes(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "errors")
	root := pushProject(t)
	withSink(t, root, "errors", filepath.Join(t.TempDir(), "x"), "")
	if code, _, errOut := runCmd("push"); code != exitCantRun || !strings.Contains(errOut, "stele-sink-errors push") {
		t.Errorf("failing sink = %d, %q", code, errOut)
	}
	withSink(t, root, "missing", filepath.Join(t.TempDir(), "x"), "")
	if code, _, errOut := runCmd("push"); code != exitCantRun || !strings.Contains(errOut, "stele-sink-missing not found") {
		t.Errorf("missing sink = %d, %q", code, errOut)
	}
	if code, _, _ := runCmd("push", "extra"); code != exitCantRun {
		t.Errorf("push extra = %d", code)
	}
}

func TestPushNoKeyNoLog(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "good")
	root := project(t, rules)
	withSink(t, root, "good", filepath.Join(t.TempDir(), "x"), "")
	code, out, errOut := runCmd("push")
	if code != exitOK || !strings.Contains(out, "no log on this machine") {
		t.Errorf("push = %d, %q, %q", code, out, errOut)
	}
}

// stele hook and stele stats never reach stele push; stele push reaches
// plugins, never the network (D82, D83).
func TestPushReach(t *testing.T) {
	for _, fn := range []string{"runHook", "runStats"} {
		if declReach(t, fn)[module+"/internal/push"] {
			t.Errorf("%s reaches internal/push", fn)
		}
	}
	reach := declReach(t, "runPush")
	if !reach[module+"/internal/pluginexec"] {
		t.Error("the walk does not see runPush reach pluginexec")
	}
	for _, p := range callReach(t, "runPush") {
		if p == "net" || strings.HasPrefix(p, "net/") || p == module+"/internal/publish" {
			t.Errorf("runPush reaches %s", p)
		}
	}
}
