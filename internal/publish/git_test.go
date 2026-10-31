package publish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testGit returns a gitCmd for the git on PATH, or skips.
func testGit(t *testing.T) gitCmd {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	return gitCmd{bin: p}
}

// setLocalRemote sets localRemote for one test.
func setLocalRemote(t *testing.T, url string) {
	t.Helper()
	old := localRemote
	localRemote = url
	t.Cleanup(func() { localRemote = old })
}

func TestGitSafetyConfig(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	out, err := g.run(ctx, dir, nil, nil, "config", "--get", "protocol.allow")
	if err != nil || strings.TrimSpace(out) != "never" {
		t.Errorf("protocol.allow = %q, %v; want never", out, err)
	}
	if out, err := g.run(ctx, dir, nil, nil, "config", "--get", "protocol.file.allow"); err == nil {
		t.Errorf("protocol.file.allow = %q, want unset", out)
	}
	setLocalRemote(t, dir)
	out, err = g.run(ctx, dir, nil, nil, "config", "--get", "protocol.file.allow")
	if err != nil || strings.TrimSpace(out) != "always" {
		t.Errorf("with localRemote, protocol.file.allow = %q, %v; want always", out, err)
	}
}

func TestGitRefusesLocalTransport(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	bare := t.TempDir()
	if _, err := g.run(ctx, bare, nil, nil, "init", "--bare", "--quiet"); err != nil {
		t.Fatal(err)
	}
	_, err := g.run(ctx, t.TempDir(), nil, nil, "ls-remote", "--", bare)
	if !errors.Is(err, ErrGit) {
		t.Fatalf("ls-remote of a local path: err = %v, want ErrGit", err)
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("err = %q, want it to say the transport is not allowed", err)
	}
}

func TestGitEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	env := strings.Join(gitCmd{}.environ([]string{"X=1"}), "\n")
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "X=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "GIT_DIR=") {
		t.Error("env keeps GIT_DIR")
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
	if _, err := g.run(context.Background(), t.TempDir(), nil, nil, "version"); err != nil {
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

func TestGitMessage(t *testing.T) {
	in := "remote: Permission to me/g.git denied to x.\n" +
		"remote: Create a pull request: https://evil.example/x\n" +
		"fatal: unable to access 'https://github.com/me/g.git/': \x1b[31mThe requested URL returned error: 403\n"
	got := gitMessage(in)
	want := "remote: Permission to me/g.git denied to x.\n" +
		"fatal: unable to access 'https://github.com/me/g.git/': The requested URL returned error: 403"
	if got != want {
		t.Errorf("gitMessage =\n%s\nwant\n%s", got, want)
	}
}

func TestGitOutputIsCapped(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	gitT(t, dir, "init", "--quiet")
	big := filepath.Join(dir, "big")
	writeFile(t, big, strings.Repeat("x", 3*maxOutput))
	blob := gitT(t, dir, "hash-object", "-w", "big")
	out, err := g.run(ctx, dir, nil, nil, "cat-file", "blob", blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != maxOutput {
		t.Errorf("run kept %d bytes, want %d", len(out), maxOutput)
	}
	out, err = g.runLimit(ctx, dir, nil, nil, 4*maxOutput, "cat-file", "blob", blob)
	if err != nil || len(out) != 3*maxOutput {
		t.Errorf("runLimit kept %d bytes, %v; want all %d", len(out), err, 3*maxOutput)
	}
}

func TestCappedWriter(t *testing.T) {
	w := &capped{max: 4}
	for _, s := range []string{"ab", "cdef", "gh"} {
		if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
			t.Errorf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if w.String() != "abcd" {
		t.Errorf("kept %q, want abcd", w.String())
	}
}

func TestGitErrorExitCode(t *testing.T) {
	g := testGit(t)
	_, err := g.run(context.Background(), t.TempDir(), nil, nil, "config", "--get", "no.such.key")
	if !errors.Is(err, ErrGit) || exitCode(err) != 1 {
		t.Errorf("err = %v, code %d; want ErrGit, 1", err, exitCode(err))
	}
}
