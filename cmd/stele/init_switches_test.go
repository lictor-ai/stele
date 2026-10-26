package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFile returns the file at path, failing the test when it cannot.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitSwitches(t *testing.T) {
	root := project(t, "")
	setVersion(t, "v0.3.0")
	settings := filepath.Join(root, ".claude", "settings.json")
	lastLine := func(out string) string {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		return lines[len(lines)-1]
	}

	code, out, errOut := runCmd("init")
	if code != exitOK || lastLine(out) != "hooks: remind on, check on" {
		t.Fatalf("init = %d, stdout %q, stderr %q", code, out, errOut)
	}

	code, out, errOut = runCmd("init", "--remind=off")
	if code != exitOK || lastLine(out) != "hooks: remind off, check on" || !strings.Contains(out, "updated stele hooks in ") {
		t.Errorf("init --remind=off = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if got := readFile(t, settings); !strings.Contains(got, `"stele hook --installed=v0.3.0 --remind=off"`) {
		t.Errorf("settings.json:\n%s", got)
	}

	code, out, _ = runCmd("init")
	if code != exitOK || !strings.Contains(out, "up to date, nothing written") || lastLine(out) != "hooks: remind off, check on" {
		t.Errorf("init again = %d, stdout %q", code, out)
	}

	code, out, _ = runCmd("init", "--check=off", "--remind=on")
	if code != exitOK || lastLine(out) != "hooks: remind on, check off" {
		t.Errorf("init --check=off --remind=on = %d, stdout %q", code, out)
	}

	code, out, errOut = runCmd("init", "--remind=off", "--check=off")
	if code != exitOK {
		t.Fatalf("init both off = %d, %q", code, errOut)
	}
	code, out, errOut = runCmd("uninstall")
	if code != exitOK || !strings.Contains(out, "removed stele hooks") {
		t.Errorf("uninstall = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if data, err := os.ReadFile(settings); err == nil && strings.Contains(string(data), "stele") {
		t.Errorf("uninstall left a stele handler:\n%s", data)
	}
}

func TestInitBadSwitch(t *testing.T) {
	root := project(t, "")
	settings := filepath.Join(root, ".claude", "settings.json")

	code, out, errOut := runCmd("init", "--remind=maybe")
	if code != exitCantRun || out != "" || !strings.Contains(errOut, "remind") {
		t.Errorf("init --remind=maybe = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("init --remind=maybe wrote %s: %v", settings, err)
	}

	if code, _, errOut := runCmd("init"); code != exitOK {
		t.Fatalf("init = %d, %q", code, errOut)
	}
	before := readFile(t, settings)
	if code, _, _ := runCmd("init", "--check=no"); code != exitCantRun {
		t.Errorf("init --check=no = %d, want %d", code, exitCantRun)
	}
	if got := readFile(t, settings); got != before {
		t.Errorf("init --check=no changed settings.json:\n%s", got)
	}
}

func TestInitNotesBackstopKey(t *testing.T) {
	root := project(t, "")
	const policy = `{"backstop": false}`
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCmd("init")
	if code != exitOK || !strings.Contains(errOut, "stele init: stele.json: backstop is no longer read; use stele init --remind=off") {
		t.Errorf("init = %d, stderr %q", code, errOut)
	}
	if got := readFile(t, filepath.Join(root, "stele.json")); got != policy {
		t.Errorf("stele.json changed: %q", got)
	}
}
