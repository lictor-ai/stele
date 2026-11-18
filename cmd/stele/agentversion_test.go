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
// prints its working directory and NoDefaultCurrentDirectoryInExePath, one
// per line.
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
	fmt.Fprintf(w, "%s\n%s\n", wd, os.Getenv("NoDefaultCurrentDirectoryInExePath"))
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

// The probe runs outside the project, as plugins and scanners do, even when
// stele init runs in it: on Windows cmd.exe, running npm's codex.cmd, looks
// for node in the working directory first.
func TestProbeRunsOutsideTheProject(t *testing.T) {
	installProbe(t)
	old := probeTimeout
	probeTimeout = 2 * time.Minute
	t.Cleanup(func() { probeTimeout = old })
	root := project(t, "")
	t.Chdir(root)
	out, err := runProbe([]string{fakeProbe}, root)
	if err != nil {
		t.Fatalf("runProbe: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("probe printed %q, want two lines", out)
	}
	wd := filepath.ToSlash(strings.TrimSpace(lines[0]))
	top := filepath.ToSlash(root)
	if strings.EqualFold(wd, top) || strings.HasPrefix(strings.ToLower(wd), strings.ToLower(top)+"/") {
		t.Errorf("probe ran in %s, inside the project %s", wd, top)
	}
	got, want := strings.TrimSpace(lines[1]), ""
	if runtime.GOOS == "windows" {
		want = "1"
	}
	if got != want {
		t.Errorf("NoDefaultCurrentDirectoryInExePath = %q, want %q", got, want)
	}
}

func TestProbeDirIsOutsideTheProject(t *testing.T) {
	root := project(t, "")
	dir, err := probeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(filepath.ToSlash(rel), "..") {
		t.Errorf("probeDir = %s, inside the project %s", filepath.ToSlash(dir), filepath.ToSlash(root))
	}
}
