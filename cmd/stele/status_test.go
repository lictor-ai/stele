package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/status"
)

func statusJSON(t *testing.T, args ...string) status.Status {
	t.Helper()
	code, out, errOut := runCmd(append([]string{"status", "--format", "json"}, args...)...)
	if code != exitOK {
		t.Fatalf("status = %d: %s", code, errOut)
	}
	var st status.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --format json is not JSON: %v\n%s", err, out)
	}
	return st
}

func TestStatusJSON(t *testing.T) {
	root := compiled(t, rules)
	st := statusJSON(t)
	if st.V != status.Version || st.Project != projectID(t, root) {
		t.Errorf("v %d, project %q", st.V, st.Project)
	}
	if len(st.Sources) != 1 || st.Sources[0].Name != "stelemd" || st.Sources[0].Rules != 2 || st.Sources[0].Fresh == nil || !*st.Sources[0].Fresh {
		t.Errorf("sources = %+v", st.Sources)
	}
	if st.Index.Fresh == nil || !*st.Index.Fresh {
		t.Errorf("index = %+v", st.Index)
	}
	if len(st.Agents) != len(agents) {
		t.Errorf("agents = %+v", st.Agents)
	}
}

func TestStatusIndexStaleAfterAnEdit(t *testing.T) {
	root := compiled(t, rules)
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(rules+"\n---\n\nA third rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := statusJSON(t)
	if st.Index.Fresh == nil || *st.Index.Fresh {
		t.Errorf("index after an edit = %+v, want stale", st.Index)
	}
}

func TestStatusText(t *testing.T) {
	root := compiled(t, rules)
	code, out, errOut := runCmd("status")
	if code != exitOK {
		t.Fatalf("status = %d: %s", code, errOut)
	}
	for _, want := range []string{"project " + projectID(t, root), "agents", "plugins", "stelemd", "regex", "2 rules, fresh", "index: fresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusPluginsOnAndOffline(t *testing.T) {
	installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "witness")
	witness := filepath.Join(t.TempDir(), "witness")
	t.Setenv("STELE_FAKE_WITNESS", witness)
	root := project(t, hookRules)
	withPluginSource(t, root, "witness", "v1", pluginRule)
	writeAt(t, root, "stele.json", `{"sources": [{"name": "witness", "config": {"fingerprint": "v1", "rules": [`+pluginRule+`]}}], "checkers": [{"name": "missing"}]}`+"\n")
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile: %s", errOut)
	}
	os.Remove(witness)

	st := statusJSON(t, "--offline")
	if data, err := os.ReadFile(witness); err == nil {
		t.Errorf("--offline started a plugin: %q", data)
	}
	for _, p := range st.Plugins {
		if !p.Builtin && p.Found != nil {
			t.Errorf("--offline looked %s up: %+v", p.Name, p)
		}
	}

	st = statusJSON(t)
	byName := map[string]status.Plugin{}
	for _, p := range st.Plugins {
		byName[p.Name] = p
	}
	if p := byName["witness"]; p.Found == nil || !*p.Found || p.Version == "" {
		t.Errorf("witness = %+v", p)
	}
	if p := byName["missing"]; p.Found == nil || *p.Found || !strings.Contains(p.Error, "stele-checker-missing") {
		t.Errorf("missing = %+v", p)
	}
	var fresh *bool
	for _, s := range st.Sources {
		if s.Name == "witness" {
			fresh = s.Fresh
		}
	}
	if fresh == nil || !*fresh {
		t.Errorf("the plugin source's freshness = %v, want fresh", fresh)
	}
	if data, _ := os.ReadFile(witness); !strings.Contains(string(data), "fingerprint") {
		t.Errorf("witness = %q, want a fingerprint call", data)
	}
}

func TestStatusBadArgs(t *testing.T) {
	project(t, rules)
	for _, args := range [][]string{{"status", "--format", "xml"}, {"status", "extra"}} {
		if code, _, _ := runCmd(args...); code != exitCantRun {
			t.Errorf("%v = %d, want %d", args, code, exitCantRun)
		}
	}
}

// stele hook and stele stats never reach stele status (D85).
func TestHookAndStatsNeverReachStatus(t *testing.T) {
	for _, fn := range []string{"runHook", "runStats"} {
		reach := declReach(t, fn)
		if reach[module+"/internal/status"] {
			t.Errorf("%s reaches internal/status", fn)
		}
	}
}
