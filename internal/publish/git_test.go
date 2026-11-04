package publish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testGit returns a gitCmd for the git on PATH, or skips. It may use the
// file transport, as Run does when localRemote is set.
func testGit(t *testing.T) gitCmd {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	return gitCmd{Bin: p, AllowFile: true}
}

// setLocalRemote sets localRemote for one test.
func setLocalRemote(t *testing.T, url string) {
	t.Helper()
	old := localRemote
	localRemote = url
	t.Cleanup(func() { localRemote = old })
}

func TestNewGitCmdAllowsFileOnlyWithLocalRemote(t *testing.T) {
	if newGitCmd("git", t.TempDir()).AllowFile {
		t.Error("AllowFile set with no localRemote")
	}
	setLocalRemote(t, t.TempDir())
	if !newGitCmd("git", t.TempDir()).AllowFile {
		t.Error("AllowFile not set with localRemote")
	}
}

// git finds ssh on PATH: an entry inside the project would run what a cloned
// repository put there, even with git itself vetted.
func TestGitDropsPathInsideProject(t *testing.T) {
	dir, log := fakeTools(t, "git")
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+bin)
	g := newGitCmd(filepath.Join(dir, "git"), root)
	if _, err := g.Run(context.Background(), t.TempDir(), nil, nil, "version"); err != nil {
		t.Fatal(err)
	}
	calls := fakeCalls(t, log)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if got := calls[0].Env["PATH"]; got != dir {
		t.Errorf("PATH = %q, want %q", got, dir)
	}
}

func TestTokenCLIDropsPathInsideProject(t *testing.T) {
	dir, log := fakeTools(t, "gh")
	root := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+filepath.Join(root, "bin"))
	cliToken(context.Background(), root, t.TempDir(), []string{"gh", "auth", "token"})
	calls := fakeCalls(t, log)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if got := calls[0].Env["PATH"]; got != dir {
		t.Errorf("PATH = %q, want %q", got, dir)
	}
}
