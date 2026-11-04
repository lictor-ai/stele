package gitexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain makes every test hermetic: git reads a global config made here
// and no system config, so Git for Windows' credential manager and the
// user's own settings never run.
func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "stele-gitexec-test")
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
	return m.Run()
}

// testGit returns a Cmd for the git on PATH, or skips.
func testGit(t *testing.T) Cmd {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	return Cmd{Bin: p}
}

// gitT runs the plain git on PATH in dir, for setting up a test, and returns
// its trimmed output.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
