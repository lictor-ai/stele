package publish

import (
	"net/url"
	"strings"

	"github.com/lictor-ai/stele/internal/gitexec"
)

// prefilledPage is the host's page to open a merge request from branch into
// base by hand, or "" for a host other than github.com and gitlab.com. stele
// builds it itself, percent-encoding every part, and never uses a link the
// remote sent.
func prefilledPage(r remote, base, branch string) string {
	switch r.Kind {
	case github:
		owner, name, ok := r.githubRepo()
		if !ok {
			return ""
		}
		return "https://" + r.Host + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) +
			"/compare/" + escapeRef(base) + "..." + escapeRef(branch) + "?expand=1"
	case gitlab:
		if !r.gitlabProject() {
			return ""
		}
		return "https://" + r.Host + "/" + escapeRef(r.Project) +
			"/-/merge_requests/new?" + url.QueryEscape("merge_request[source_branch]") + "=" + url.QueryEscape(branch)
	}
	return ""
}

// escapeRef percent-encodes each part of a slash-separated name, keeping the
// slashes.
func escapeRef(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// sameHostHTTPS reports whether link is an https URL on exactly host, with no
// user and no port. A link from the API is used only then.
func sameHostHTTPS(link, host string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.User == nil && u.Host == host && u.Opaque == ""
}

// hostOf returns the host of link, or "".
func hostOf(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Host
}

// Clean makes s one printable line (gitexec.Clean).
func Clean(s string) string { return gitexec.Clean(s) }

// CleanLines cleans each line of s with Clean and keeps the line breaks: for
// text meant to span lines, such as git's message when it fails.
func CleanLines(s string) string { return gitexec.CleanLines(s) }
