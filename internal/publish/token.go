package publish

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tokenTimeout bounds one attempt to read a stored login.
var tokenTimeout = 10 * time.Second

// tokenSource is one place a login may already be stored.
type tokenSource struct {
	name string                           // for messages: "gh", "git credential", "GITHUB_TOKEN"
	get  func(ctx context.Context) string // the token, or "" if there is none
}

// tokenSources lists, in order, where stele looks for a login to r's host.
// It is empty for any host but github.com and gitlab.com: no other host ever
// receives a token (D65). Each source runs only when the one before it found
// nothing or was rejected. Tests replace it.
var tokenSources = defaultTokenSources

// tokenEnv are the variables a host's CLI reads a token from before its own
// login. They are removed, so the CLI answers with its stored login only; the
// environment is tried on its own, last.
var tokenEnv = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST",
	"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "GITLAB_HOST", "GL_HOST",
}

func defaultTokenSources(g gitCmd, root, dir string, r remote) []tokenSource {
	var cli []string
	var envName string
	switch r.Kind {
	case github:
		cli = []string{"gh", "auth", "token", "--hostname", "github.com"}
		envName = "GITHUB_TOKEN"
	case gitlab:
		cli = []string{"glab", "config", "get", "token", "--host", "gitlab.com"}
		envName = "GITLAB_TOKEN"
	default:
		return nil
	}
	return []tokenSource{
		{cli[0], func(ctx context.Context) string { return cliToken(ctx, root, dir, cli) }},
		{"git credential", func(ctx context.Context) string { return credentialToken(ctx, g, dir, r.Host) }},
		{envName, func(context.Context) string { return strings.TrimSpace(os.Getenv(envName)) }},
	}
}

// cliToken asks gh or glab for its stored login. The CLI is found on PATH,
// never inside the project, and runs outside the project with no token in
// its environment and nothing on stdin.
func cliToken(ctx context.Context, root, dir string, argv []string) string {
	bin, err := resolveTool(argv[0], root)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = withoutTokens(os.Environ())
	out := &capped{max: maxOutput}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return firstLine(out.String())
}

func withoutTokens(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, t := range tokenEnv {
			if strings.EqualFold(name, t) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// credentialToken asks git's credential helpers for what they store for
// https://host, without any prompt. Git Credential Manager stores an OAuth
// token there. stele never tells git whether it worked: it neither approves
// nor rejects the credential.
func credentialToken(ctx context.Context, g gitCmd, dir, host string) string {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	out, err := g.run(ctx, dir, nil, []byte("protocol=https\nhost="+host+"\n\n"), "credential", "fill")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "password="); ok {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
