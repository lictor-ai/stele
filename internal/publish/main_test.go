package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain makes every test hermetic: git reads a global config made here
// and no system config, so Git for Windows' credential manager and the
// user's own settings never run; the cache lives in a temporary directory.
func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "stele-publish-test")
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
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "GH_TOKEN", "GITLAB_ACCESS_TOKEN"} {
		os.Unsetenv(name)
	}
	return m.Run()
}
