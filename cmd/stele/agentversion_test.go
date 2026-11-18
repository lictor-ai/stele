package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeProbe is the name this test binary answers a version probe under: it
// prints its working directory and NoDefaultCurrentDirectoryInExePath, as
// cwd=... and nocwd=... lines.
const fakeProbe = "agentprobe"

// isFakeProbe reports whether exe, os.Args[0], is the fake probe.
func isFakeProbe(exe string) bool {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe") == fakeProbe
}

// serveProbe is the fake probe's main.
func serveProbe(w io.Writer) int {
	wd, err := os.Getwd()
	if err != nil {
		return 1
	}
	fmt.Fprintf(w, "cwd=%s\nnocwd=%s\n", wd, os.Getenv("NoDefaultCurrentDirectoryInExePath"))
	return 0
}

// installProbe puts this test binary on PATH, in a folder outside any
// project, as the fake probe.
func installProbe(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name := fakeProbe
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.Link(os.Args[0], dst); err != nil {
		data, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// probeLine returns the value of the line of out that starts with key=.
func probeLine(t *testing.T, out []byte, key string) string {
	t.Helper()
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), key+"="); ok {
			return v
		}
	}
	t.Fatalf("probe printed %q, no %s= line", out, key)
	return ""
}

// The probe runs from a fresh folder outside the project, like plugins and
// scanners, even when stele init runs in the project, and the folder goes
// once it is done.
func TestProbeRunsOutsideTheProject(t *testing.T) {
	installProbe(t)
	old := probeTimeout
	probeTimeout = 2 * time.Minute
	t.Cleanup(func() { probeTimeout = old })
	// Whatever this machine has set, the probe's env decides.
	t.Setenv("NoDefaultCurrentDirectoryInExePath", "")
	os.Unsetenv("NoDefaultCurrentDirectoryInExePath")
	root := project(t, "")
	t.Chdir(root)
	out, err := runProbe([]string{fakeProbe}, root)
	if err != nil {
		t.Fatalf("runProbe: %v", err)
	}
	wd := probeLine(t, out, "cwd")
	if rel, err := filepath.Rel(root, wd); err == nil && !strings.HasPrefix(filepath.ToSlash(rel), "..") {
		t.Errorf("probe ran in %s, inside the project %s", filepath.ToSlash(wd), filepath.ToSlash(root))
	}
	if !strings.HasPrefix(filepath.Base(wd), "stele-probe-") {
		t.Errorf("probe ran in %s, want a fresh stele-probe- folder", filepath.ToSlash(wd))
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Errorf("probe folder %s is still there: %v", filepath.ToSlash(wd), err)
	}
	got := probeLine(t, out, "nocwd")
	if runtime.GOOS == "windows" && got == "" {
		t.Error("NoDefaultCurrentDirectoryInExePath is not set for the probe on Windows")
	}
	if runtime.GOOS != "windows" && got != "" {
		t.Errorf("NoDefaultCurrentDirectoryInExePath = %q, want it unset", got)
	}
}

func TestProbeDirIsOutsideTheProject(t *testing.T) {
	root := project(t, "")
	dir, err := probeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(filepath.ToSlash(rel), "..") {
		t.Errorf("probeDir = %s, inside the project %s", filepath.ToSlash(dir), filepath.ToSlash(root))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("probeDir = %s: %d entries, %v; want an empty folder", filepath.ToSlash(dir), len(entries), err)
	}
}
