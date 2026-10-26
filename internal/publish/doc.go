// Package publish sends a project's stele.md to a git repository as a merge
// request (D64–D66, ADR-0007).
//
// The developer names the repository and a path inside it under "publish" in
// stele.json. Run reads the committed stele.md from the project's git, commits
// it on the repository's default branch in a bare clone under the user's
// cache directory, pushes it to the branch stele/publish/<path>, and opens a
// merge request on github.com or gitlab.com with a login the developer already
// has. Any other host gets a page to open it by hand.
//
// This is the only package that imports net/http, and one of two that start
// processes. stele hook never reaches it (D45). What leaves the machine is
// stele.md, its path, the project's commit hash and id, the author's git
// identity and, to github.com or gitlab.com only, a token (D66).
//
// Like every package under internal/, it never prints or exits: cmd/stele
// does.
package publish
