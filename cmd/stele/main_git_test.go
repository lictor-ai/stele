package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/plugin/plugintest"
	"github.com/lictor-ai/stele/internal/pluginexec"
)

// TestMain makes every git the tests run, and every git stele runs for
// them, hermetic: a global config made here and no system config, so the
// user's settings, hooks and credential helpers never apply. Started under a
// plugin's name, this test binary is the fake plugin (plugintest).
func TestMain(m *testing.M) {
	if plugintest.Is(os.Args[0]) {
		os.Exit(plugintest.Serve(os.Args[0], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "stele-cmd-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	global := filepath.Join(dir, "gitconfig")
	config := "[user]\n\tname = t\n\temail = t@example.com\n[commit]\n\tgpgsign = false\n[core]\n\tautocrlf = false\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Setenv("GIT_CONFIG_GLOBAL", global)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// Plugins run in a directory of their own: here, not the user's cache.
	pluginexec.WorkBase = func() (string, error) { return dir, nil }
	return m.Run()
}

func TestGitConfigIsHermetic(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	// Outside any repository, as the tests' projects are.
	cmd := exec.Command("git", "config", "--show-origin", "--get", "user.email")
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "stele-cmd-test") {
		t.Errorf("user.email comes from %q, %v; want the test's own config", out, err)
	}
}
