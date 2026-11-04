package gitexec

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	hostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	port     = regexp.MustCompile(`^[0-9]{1,5}$`)
	// scp is git's user@host:path form. The user is required, so a local
	// path such as C:/x is never read as one.
	scp = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9][A-Za-z0-9.-]*):([^/:\-].*)$`)
)

// repoForms is what ValidateRepo accepts, for its messages.
const repoForms = "use https://host/path, ssh://host/path or user@host:path"

// ValidateRepo accepts https://host/path, ssh://host/path and user@host:path,
// and nothing else: no local path, no file:// or ext::, no option, no
// password. "repo" comes from stele.json, which a cloned repository chooses.
// The error names repo but never a password; the caller says which setting
// it is.
func ValidateRepo(repo string) error {
	bad := func(why string) error {
		return fmt.Errorf("repo %q: %s; %s", repo, why, repoForms)
	}
	if repo == "" {
		return errors.New("repo is empty; " + repoForms)
	}
	for _, r := range repo {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("repo has a space or a control character")
		}
	}
	if strings.HasPrefix(repo, "-") {
		return bad("it starts with -")
	}
	if !strings.Contains(repo, "://") {
		if at := strings.Index(repo, "@"); at > 0 && strings.Contains(repo[:at], ":") {
			return errors.New("repo holds a password; remove it and let git's own login supply it")
		}
		m := scp.FindStringSubmatch(repo)
		if m == nil {
			return bad("not a remote URL")
		}
		return nil
	}
	u, err := url.Parse(repo)
	if err != nil {
		if strings.Contains(repo, "@") {
			return errors.New("repo is not a valid URL")
		}
		return bad("not a valid URL")
	}
	if _, has := u.User.Password(); has {
		return errors.New("repo holds a password; remove it and let git's own login supply it")
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return bad("only https and ssh are allowed")
	}
	if !hostname.MatchString(u.Hostname()) {
		return bad("no valid host")
	}
	if p := u.Port(); p != "" && !port.MatchString(p) {
		return bad("bad port")
	}
	if u.Path == "" || u.Path == "/" {
		return bad("no repository path")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return bad("a query or fragment")
	}
	return nil
}

// HostPath returns the host of a repo ValidateRepo accepted, without user or
// port, and the repository's path on it, without slashes around it or .git.
func HostPath(repo string) (host, path string) {
	if m := scp.FindStringSubmatch(repo); m != nil && !strings.Contains(repo, "://") {
		host, path = m[2], m[3]
	} else if u, err := url.Parse(repo); err == nil {
		host, path = u.Hostname(), u.Path
	}
	path = strings.Trim(path, "/")
	return host, strings.TrimSuffix(path, ".git")
}
