package publish

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// tokens runs every source in order and returns what each found.
func tokens(srcs []tokenSource) []string {
	var out []string
	for _, s := range srcs {
		out = append(out, s.name+"="+s.get(context.Background()))
	}
	return out
}

func TestTokenSourcesGitHub(t *testing.T) {
	bin, log := fakeTools(t, "gh", "git")
	root, dir := t.TempDir(), t.TempDir()
	t.Setenv("FAKE_GH_OUT", "gh-token\n")
	t.Setenv("FAKE_GIT_OUT", "protocol=https\nhost=github.com\nusername=x\npassword=cred-token\n")
	t.Setenv("GITHUB_TOKEN", "env-token")
	t.Setenv("GH_TOKEN", "must-not-reach-gh")
	t.Setenv("GITLAB_TOKEN", "gitlab-only")
	g := gitCmd{bin: filepath.Join(bin, "git")}

	got := tokens(defaultTokenSources(g, root, dir, parseRemote("git@github.com:me/g.git")))
	want := []string{"gh=gh-token", "git credential=cred-token", "GITHUB_TOKEN=env-token"}
	if !slices.Equal(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
	calls := fakeCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	gh, cred := calls[0], calls[1]
	if !slices.Equal(gh.Args, []string{"auth", "token", "--hostname", "github.com"}) {
		t.Errorf("gh args = %q", gh.Args)
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN"} {
		if _, ok := gh.Env[name]; ok {
			t.Errorf("gh got %s", name)
		}
	}
	if !samePath(gh.Dir, dir) || !samePath(cred.Dir, dir) {
		t.Errorf("dirs = %q, %q; want %q", gh.Dir, cred.Dir, dir)
	}
	if !slices.Contains(cred.Args, "credential") || cred.Args[len(cred.Args)-1] != "fill" {
		t.Errorf("git args = %q", cred.Args)
	}
	if cred.Stdin != "protocol=https\nhost=github.com\n\n" {
		t.Errorf("credential stdin = %q", cred.Stdin)
	}
	if cred.Env["GIT_TERMINAL_PROMPT"] != "0" || cred.Env["GCM_INTERACTIVE"] != "never" {
		t.Errorf("credential fill may prompt: env %v", cred.Env)
	}
}

func TestTokenSourcesGitLab(t *testing.T) {
	bin, log := fakeTools(t, "glab", "git")
	t.Setenv("FAKE_GLAB_OUT", "glab-token\n")
	t.Setenv("FAKE_GIT_EXIT", "128")
	t.Setenv("GITLAB_TOKEN", "env-token")
	t.Setenv("GITHUB_TOKEN", "github-only")
	g := gitCmd{bin: filepath.Join(bin, "git")}

	got := tokens(defaultTokenSources(g, t.TempDir(), t.TempDir(), parseRemote("git@gitlab.com:a/b.git")))
	want := []string{"glab=glab-token", "git credential=", "GITLAB_TOKEN=env-token"}
	if !slices.Equal(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
	if calls := fakeCalls(t, log); !slices.Equal(calls[0].Args, []string{"config", "get", "token", "--host", "gitlab.com"}) {
		t.Errorf("glab args = %q", calls[0].Args)
	}
}

func TestTokenSourcesMissingCLI(t *testing.T) {
	bin, _ := fakeTools(t, "git")
	g := gitCmd{bin: filepath.Join(bin, "git")}
	got := tokens(defaultTokenSources(g, t.TempDir(), t.TempDir(), parseRemote("git@github.com:me/g.git")))
	if got[0] != "gh=" {
		t.Errorf("missing gh: %q", got)
	}
}

func TestTokenSourcesRefuseCLIInProject(t *testing.T) {
	bin, log := fakeTools(t, "gh", "git")
	t.Setenv("FAKE_GH_OUT", "gh-token\n")
	g := gitCmd{bin: filepath.Join(bin, "git")}
	srcs := defaultTokenSources(g, filepath.Dir(bin), t.TempDir(), parseRemote("git@github.com:me/g.git"))
	if tok := srcs[0].get(context.Background()); tok != "" {
		t.Errorf("gh inside the project gave %q", tok)
	}
	if calls := fakeCalls(t, log); len(calls) != 0 {
		t.Errorf("gh inside the project ran: %+v", calls)
	}
}

func TestTokenSourcesOtherHost(t *testing.T) {
	_, log := fakeTools(t, "gh", "glab", "git")
	t.Setenv("GITHUB_TOKEN", "x")
	t.Setenv("GITLAB_TOKEN", "x")
	for _, repo := range []string{"git@gitlab.attacker.example:x/y.git", "https://GitHub.com/x/y", "https://github.com.evil.example/x/y"} {
		if srcs := defaultTokenSources(gitCmd{bin: "git"}, t.TempDir(), t.TempDir(), parseRemote(repo)); len(srcs) != 0 {
			t.Errorf("%s: %d token sources, want none", repo, len(srcs))
		}
	}
	if calls := fakeCalls(t, log); len(calls) != 0 {
		t.Errorf("calls = %+v, want none", calls)
	}
}
