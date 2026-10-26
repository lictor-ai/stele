package publish

import "testing"

func TestParseRemote(t *testing.T) {
	tests := []struct {
		repo      string
		kind      kind
		host      string
		project   string
		tokenHost bool
	}{
		{"git@github.com:me/g.git", github, "github.com", "me/g", true},
		{"https://github.com/me/g/", github, "github.com", "me/g", true},
		{"ssh://git@gitlab.com/a/b/c.git", gitlab, "gitlab.com", "a/b/c", true},
		{"https://gitlab.com:443/a/b", gitlab, "gitlab.com", "a/b", true},
		{"https://gitlab.attacker.example/x/y", other, "gitlab.attacker.example", "x/y", false},
		{"https://github.com.evil.example/x/y", other, "github.com.evil.example", "x/y", false},
		{"https://api.github.com/x/y", other, "api.github.com", "x/y", false},
		{"https://GitHub.com/x/y", other, "GitHub.com", "x/y", false},
		{"git@evilgithub.com:x/y", other, "evilgithub.com", "x/y", false},
	}
	for _, tt := range tests {
		r := parseRemote(tt.repo)
		if r.Kind != tt.kind || r.Host != tt.host || r.Project != tt.project {
			t.Errorf("parseRemote(%q) = %+v, want kind %d host %q project %q", tt.repo, r, tt.kind, tt.host, tt.project)
		}
		if r.tokenHost() != tt.tokenHost {
			t.Errorf("parseRemote(%q).tokenHost() = %v, want %v", tt.repo, r.tokenHost(), tt.tokenHost)
		}
	}
}

func TestGithubRepo(t *testing.T) {
	if o, n, ok := parseRemote("git@github.com:me/g.git").githubRepo(); !ok || o != "me" || n != "g" {
		t.Errorf("githubRepo = %q %q %v", o, n, ok)
	}
	for _, repo := range []string{"https://github.com/me/g/extra", "https://github.com/me"} {
		if _, _, ok := parseRemote(repo).githubRepo(); ok {
			t.Errorf("githubRepo(%q) ok, want not", repo)
		}
	}
}

func TestGitlabProject(t *testing.T) {
	if !parseRemote("git@gitlab.com:a/b/c.git").gitlabProject() {
		t.Error("a/b/c is a GitLab project")
	}
	if parseRemote("https://gitlab.com/a").gitlabProject() {
		t.Error("a alone is not a GitLab project")
	}
}
