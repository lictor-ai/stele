# Publishing the rules to a git repository

* Status: accepted
* Date: 2026-10-25

## Context and Problem Statement

A developer wants a project's `stele.md` versioned in a git repository of their
choosing, outside the project: one repository for all their projects, or one
per project. They want it as a merge request they review, not a direct push.

v1 makes no network call (ADR-0004: "no model call and no network on any
path"), and data leaves the machine only through a sink, from `stele push` or
`stele check --push` (D53). Publishing needs both a push and, to open the merge
request, a call to the host's API.

## Considered Options

* The developer copies `stele.md` and pushes it by hand. stele does nothing.
* `stele publish` pushes a branch and shells out to `gh` or `glab` to open the
  merge request.
* `stele publish` pushes a branch and calls the GitHub or GitLab API itself,
  with a login the developer already has.

## Decision Outcome

Chosen option: "`stele publish` pushes a branch and calls the GitHub or GitLab
API itself", because it needs nothing installed
and works on both hosts, and reusing an existing login means stele never asks
for a token. `gh` covers only GitHub and `glab` only GitLab, and either would
be one more install. git-spice does the same from a Go binary.

The design is `docs/superpowers/specs/2026-10-25-stele-publish-design.md`. It
amends:

* **D53.** `stele publish` is a second way out. It runs only when the developer
  runs it, sends only to the `repo` in `stele.json`, and sends only
  `stele.md`, its `path`, the project's commit hash and id, and the author's
  identity.
* **ADR-0004, "no network on any path".** Network calls are allowed in
  `internal/publish` only. The hook, compile and check stay offline.
* **D40.** `stele publish` reads `stele.md` from git to copy it. It never
  parses it.

The spec carries these as D64–D66.

### Positive Consequences

* A project's rules are versioned and reviewed in a repository the developer
  picks, with one command.
* No new dependency and no new token.

### Negative Consequences

* stele now has network code, and handles tokens. It must send them only to
  `github.com` and `gitlab.com`, because `stele.json`, and so `repo`, comes
  from a repository that may be hostile.
* Self-hosted GitHub and GitLab get a prefilled page, not a merge request.
