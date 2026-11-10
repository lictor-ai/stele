package extcheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain makes this test binary a scanner that never answers when it is
// started as "sleeper", for the timeout test.
func TestMain(m *testing.M) {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "sleeper" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// copyExe puts this test binary in dir as name, and returns its path.
func copyExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A scanner that doesn't answer in time is stopped, and that is an error,
// never an empty scan.
func TestRunStopsAtTheDeadline(t *testing.T) {
	bin := t.TempDir()
	copyExe(t, bin, "sleeper")
	t.Setenv("PATH", bin)
	tool, err := FindTool("sleeper", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, _, _, err := tool.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "sleeper stopped") || out != nil {
		t.Errorf("Run = %q, %v; want stopped", out, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Run took %v after its deadline", d)
	}
}

// On Windows a scanner that is only a .bat or .cmd would run through
// cmd.exe: it is refused, and says so.
func TestFindToolRefusesScriptsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows runs a script through a shell by its extension")
	}
	bin := t.TempDir()
	for _, n := range []string{"scanner.bat", "scanner.cmd"} {
		os.WriteFile(filepath.Join(bin, n), []byte("@echo off\r\n"), 0o755)
	}
	t.Setenv("PATH", bin)
	_, err := FindTool("scanner", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "is refused") || !strings.Contains(err.Error(), "scanner.bat") {
		t.Errorf("FindTool = %v, want the .bat refused", err)
	}
}

// mklinkJ makes link a junction to target, or skips the test.
func mklinkJ(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
}

// filepath.EvalSymlinks doesn't follow a junction; ProjectFile does: a
// project opened through one works, and a junction out of the project is
// caught.
func TestProjectFileThroughJunctions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows'")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(real, "r", "a.yml"), []byte("rules: []\n"), 0o644)
	link := filepath.Join(base, "link")
	mklinkJ(t, link, real)
	if _, err := ProjectFile(link, "r/a.yml"); err != nil {
		t.Errorf("ProjectFile through a junction to the project = %v", err)
	}

	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "x.yml"), []byte("rules: []\n"), 0o644)
	mklinkJ(t, filepath.Join(real, "out"), outside)
	if _, err := ProjectFile(real, "out/x.yml"); !errors.Is(err, ErrOutside) {
		t.Errorf("ProjectFile through a junction out of the project = %v, want ErrOutside", err)
	}
	if _, err := ProjectFile(link, "out/x.yml"); !errors.Is(err, ErrOutside) {
		t.Errorf("ProjectFile through both junctions = %v, want ErrOutside", err)
	}
}
