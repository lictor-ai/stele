package gitexec

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitSafetyConfig(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	out, err := g.Run(ctx, dir, nil, nil, "config", "--get", "protocol.allow")
	if err != nil || strings.TrimSpace(out) != "never" {
		t.Errorf("protocol.allow = %q, %v; want never", out, err)
	}
	if out, err := g.Run(ctx, dir, nil, nil, "config", "--get", "protocol.file.allow"); err == nil {
		t.Errorf("protocol.file.allow = %q, want unset", out)
	}
	g.AllowFile = true
	out, err = g.Run(ctx, dir, nil, nil, "config", "--get", "protocol.file.allow")
	if err != nil || strings.TrimSpace(out) != "always" {
		t.Errorf("with AllowFile, protocol.file.allow = %q, %v; want always", out, err)
	}
}

func TestGitRefusesLocalTransport(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	bare := t.TempDir()
	if _, err := g.Run(ctx, bare, nil, nil, "init", "--bare", "--quiet"); err != nil {
		t.Fatal(err)
	}
	_, err := g.Run(ctx, t.TempDir(), nil, nil, "ls-remote", "--", bare)
	if !errors.Is(err, ErrGit) {
		t.Fatalf("ls-remote of a local path: err = %v, want ErrGit", err)
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("err = %q, want it to say the transport is not allowed", err)
	}
}

func TestGitEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	env := strings.Join(Cmd{}.Environ([]string{"X=1"}), "\n")
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
	got := Message(in)
	want := "remote: Permission to me/g.git denied to x.\n" +
		"fatal: unable to access 'https://github.com/me/g.git/': The requested URL returned error: 403"
	if got != want {
		t.Errorf("Message =\n%s\nwant\n%s", got, want)
	}
}

func TestGitOutputIsCapped(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	gitT(t, dir, "init", "--quiet")
	big := filepath.Join(dir, "big")
	writeFile(t, big, strings.Repeat("x", 3*MaxOutput))
	blob := gitT(t, dir, "hash-object", "-w", "big")
	out, err := g.Run(ctx, dir, nil, nil, "cat-file", "blob", blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != MaxOutput {
		t.Errorf("Run kept %d bytes, want %d", len(out), MaxOutput)
	}
	out, err = g.RunLimit(ctx, dir, nil, nil, 4*MaxOutput, "cat-file", "blob", blob)
	if err != nil || len(out) != 3*MaxOutput {
		t.Errorf("RunLimit kept %d bytes, %v; want all %d", len(out), err, 3*MaxOutput)
	}
}

func TestCappedWriter(t *testing.T) {
	w := &Capped{Max: 4}
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
	_, err := g.Run(context.Background(), t.TempDir(), nil, nil, "config", "--get", "no.such.key")
	if !errors.Is(err, ErrGit) || ExitCode(err) != 1 {
		t.Errorf("err = %v, code %d; want ErrGit, 1", err, ExitCode(err))
	}
}

func TestTimeoutFor(t *testing.T) {
	const net = 7 * time.Minute
	for _, args := range [][]string{
		{"clone", "--bare"}, {"--git-dir=x", "fetch"}, {"--git-dir=x", "push"}, {"ls-remote", "--symref"},
		{"-c", "transfer.fsckObjects=true", "--git-dir=x", "fetch"},
	} {
		if got := TimeoutFor(args, net); got != net {
			t.Errorf("TimeoutFor(%q) = %v, want %v", args, got, net)
		}
	}
	for _, args := range [][]string{{"--git-dir=x", "write-tree"}, {"rev-parse", "HEAD"}, {"credential", "fill"}} {
		if got := TimeoutFor(args, net); got != GitTimeout {
			t.Errorf("TimeoutFor(%q) = %v, want %v", args, got, GitTimeout)
		}
	}
}

func TestErrorNamesTheSubcommand(t *testing.T) {
	g := testGit(t)
	_, err := g.Run(context.Background(), t.TempDir(), nil, nil, "-c", "x.y=z", "--git-dir="+t.TempDir(), "cat-file", "-e", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "git cat-file:") {
		t.Errorf("err = %v, want it to name git cat-file", err)
	}
}

// stele.json is committed: a user or token in an https repo would be too.
// The error never shows it.
func TestValidateRepoRefusesHTTPSUser(t *testing.T) {
	for _, repo := range []string{
		"https://ghp_secrettoken@github.com/a/b.git",
		"https://x-access-token:ghp_secrettoken@github.com/a/b.git",
		"https://ghp_secrettoken@github.com/a/b?x",
	} {
		err := ValidateRepo(repo)
		if err == nil {
			t.Errorf("ValidateRepo(%q) accepted", repo)
			continue
		}
		if strings.Contains(err.Error(), "secrettoken") {
			t.Errorf("ValidateRepo(%q) = %q, shows the token", repo, err)
		}
	}
	if err := ValidateRepo("ssh://git@gitlab.com/g/p.git"); err != nil {
		t.Errorf("ssh with a user: %v", err)
	}
	if got := access(ErrGit, "https://tok@host/x"); strings.Contains(got.Error(), "tok@") {
		t.Errorf("access = %q, shows the user", got)
	}
}

func TestCleanLines(t *testing.T) {
	in := "fatal: \x1b[31mone\ntwo\tthree\nfour‮"
	if got, want := CleanLines(in), "fatal: one\ntwo three\nfour"; got != want {
		t.Errorf("CleanLines = %q, want %q", got, want)
	}
}

func TestHostPath(t *testing.T) {
	for repo, want := range map[string][2]string{
		"git@github.com:me/g.git":            {"github.com", "me/g"},
		"https://gitlab.com/a/b/c.git":       {"gitlab.com", "a/b/c"},
		"ssh://git@host.example:22/x/y.git/": {"host.example", "x/y"},
	} {
		host, path := HostPath(repo)
		if host != want[0] || path != want[1] {
			t.Errorf("HostPath(%q) = %q, %q; want %q, %q", repo, host, path, want[0], want[1])
		}
	}
}
