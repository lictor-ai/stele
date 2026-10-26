package publish

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
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

// csi is a terminal escape sequence: ESC [ parameters, a final letter.
var csi = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Clean makes s one printable line: it removes terminal escape sequences,
// control characters and the characters that reorder text, and turns a
// newline or a tab into a space. What git, a host or stele.json supplies can
// then neither rewrite the terminal, nor add a line that looks like stele's
// own, nor disguise a link.
func Clean(s string) string {
	s = csi.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return ' '
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F:
			return -1
		}
		return r
	}, s)
}

// CleanLines cleans each line of s with Clean and keeps the line breaks: for
// text meant to span lines, such as git's message when it fails.
func CleanLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Clean(l)
	}
	return strings.Join(lines, "\n")
}
