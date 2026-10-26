package publish

import (
	"context"
	"errors"
	"os/exec"
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
	env := strings.Join(gitEnv([]string{"X=1"}), "\n")
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "X=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "GIT_DIR=") {
		t.Error("env keeps GIT_DIR")
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

func TestGitErrorExitCode(t *testing.T) {
	g := testGit(t)
	_, err := g.run(context.Background(), t.TempDir(), nil, nil, "config", "--get", "no.such.key")
	if !errors.Is(err, ErrGit) || exitCode(err) != 1 {
		t.Errorf("err = %v, code %d; want ErrGit, 1", err, exitCode(err))
	}
}
