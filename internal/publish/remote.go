package publish

import (
	"net/url"
	"strings"
)

// kind is the host of a repository, as far as opening a merge request goes.
type kind int

const (
	other  kind = iota // any other host: the branch is pushed, nothing more
	github             // github.com
	gitlab             // gitlab.com
)

// remote is what stele needs to know of "repo" beyond the URL git uses.
type remote struct {
	Kind    kind
	Host    string // the host name, without user or port
	Project string // the repository's path on the host, without .git
}

// parseRemote reads a repo that validateRepo accepted. Only the exact host
// names github.com and gitlab.com are recognised: a token goes to nothing else
// (D65), so gitlab.attacker.example and GitHub.com are other hosts.
func parseRemote(repo string) remote {
	var host, path string
	if m := scp.FindStringSubmatch(repo); m != nil && !strings.Contains(repo, "://") {
		host, path = m[2], m[3]
	} else if u, err := url.Parse(repo); err == nil {
		host, path = u.Hostname(), u.Path
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	r := remote{Host: host, Project: path}
	switch host {
	case "github.com":
		r.Kind = github
	case "gitlab.com":
		r.Kind = gitlab
	}
	return r
}

// tokenHost reports whether this host may receive a token.
func (r remote) tokenHost() bool { return r.Kind == github || r.Kind == gitlab }

// githubRepo splits a GitHub project into owner and name. GitHub has no
// nested paths, so anything else is not a repository it can open a pull
// request on.
func (r remote) githubRepo() (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(r.Project, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

// gitlabProject reports whether the project is a GitLab path: a group, any
// subgroups and a name.
func (r remote) gitlabProject() bool {
	parts := strings.Split(r.Project, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}
