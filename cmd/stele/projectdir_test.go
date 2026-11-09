package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// echoed is every request the echo plugins wrote to $STELE_FAKE_REQUESTS, by
// op.
func echoed(t *testing.T, file string) map[string][]map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("no request echoed: %v", err)
	}
	out := map[string][]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("echoed %q: %v", l, err)
		}
		op, _ := m["op"].(string)
		out[op] = append(out[op], m)
	}
	return out
}

// echoFile points the echo plugins at a fresh file and returns it.
func echoFile(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("STELE_FAKE_REQUESTS", f)
	return f
}

// sameDir checks got, a request's path, is root: absolute and the same
// folder (ADR-0016).
func sameDir(t *testing.T, what string, got any, root string) {
	t.Helper()
	s, _ := got.(string)
	if !filepath.IsAbs(s) {
		t.Errorf("%s = %v, not absolute", what, got)
		return
	}
	if filepath.ToSlash(s) == filepath.ToSlash(root) {
		return
	}
	a, errA := os.Stat(s)
	b, errB := os.Stat(root)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Errorf("%s = %s, want %s", what, s, root)
	}
}

// wantProjectDir checks every request of op carries project_dir, the
// project, and root with the same value when withRoot, none otherwise.
func wantProjectDir(t *testing.T, reqs map[string][]map[string]any, op, root string, withRoot bool) {
	t.Helper()
	if len(reqs[op]) == 0 {
		t.Fatalf("no %s request: %v", op, reqs)
	}
	for _, m := range reqs[op] {
		sameDir(t, op+" project_dir", m["project_dir"], root)
		r, ok := m["root"]
		switch {
		case withRoot && !ok:
			t.Errorf("%s has no root: %v", op, m)
		case withRoot:
			sameDir(t, op+" root", r, root)
		case ok:
			t.Errorf("%s has a root: %v", op, m)
		}
	}
	for _, m := range reqs["describe"] {
		if _, ok := m["project_dir"]; ok {
			t.Errorf("describe has project_dir: %v", m)
		}
	}
}

func TestPluginRequestsCarryProjectDir(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		installPlugin(t, filepath.Join(t.TempDir(), "bin"), "source", "echo")
		reqs := echoFile(t)
		root := project(t, hookRules)
		withPluginSource(t, root, "echo", "v1", pluginRule)
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile: %s", errOut)
		}
		if code, _, errOut := runCmd("status"); code != exitOK {
			t.Fatalf("status: %s", errOut)
		}
		got := echoed(t, reqs)
		wantProjectDir(t, got, "read", root, true)
		wantProjectDir(t, got, "fingerprint", root, true)
	})

	t.Run("checker", func(t *testing.T) {
		installPlugin(t, filepath.Join(t.TempDir(), "bin"), "checker", "echo")
		reqs := echoFile(t)
		root := project(t, strings.ReplaceAll(checkedRules, "check: good", "check: echo"))
		withPluginChecker(t, root, "echo", "", "")
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile: %s", errOut)
		}
		writeAt(t, root, "a.go", "package a\n")
		if code, _, errOut := runCmd("check", "a.go"); code != exitOK {
			t.Fatalf("check: %s", errOut)
		}
		got := echoed(t, reqs)
		wantProjectDir(t, got, "validate", root, false)
		wantProjectDir(t, got, "check", root, true)
	})

	t.Run("push from a subdirectory", func(t *testing.T) {
		installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "echo")
		reqs := echoFile(t)
		root := pushProject(t)
		withSink(t, root, "echo", filepath.Join(t.TempDir(), "received.jsonl"), "")
		sub := filepath.Join(root, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(sub)
		if code, _, errOut := runCmd("push"); code != exitOK {
			t.Fatalf("push: %s", errOut)
		}
		wantProjectDir(t, echoed(t, reqs), "push", root, false)
	})

	t.Run("check --push in CI", func(t *testing.T) {
		installPlugin(t, filepath.Join(t.TempDir(), "bin"), "sink", "echo")
		reqs := echoFile(t)
		root := gitProject(t, checkRules)
		withSink(t, root, "echo", filepath.Join(t.TempDir(), "received.jsonl"), "")
		if err := os.RemoveAll(filepath.Join(root, ".stele")); err != nil {
			t.Fatal(err)
		}
		ci := t.TempDir()
		old := keyDir
		keyDir = func() (string, error) { return ci, nil }
		t.Cleanup(func() { keyDir = old })
		writeAt(t, root, "db.go", "package db\n")
		if code, _, errOut := runCmd("check", "--push", "db.go"); code != exitOK {
			t.Fatalf("check --push: %s", errOut)
		}
		wantProjectDir(t, echoed(t, reqs), "push", root, false)
	})
}
