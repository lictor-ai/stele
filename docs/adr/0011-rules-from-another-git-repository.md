# Rules from another git repository

* Status: accepted
* Date: 2026-11-04
* Amends: ADR-0004 ("no network on any path": `stele compile`, `stele check`
  and `stele init` may fetch a listed repository); ADR-0007 ("the hook,
  compile and check stay offline", the same); spec D45 (`Builtins()` are the
  hot-path sources, and a source that starts a process is never one), D53 and
  D66 (a fetch brings rules in and sends nothing of the project), D62 (the
  rules file also holds the shared rules), §2.4 and §11 ("sources other than
  `stele.md`")

## Context and Problem Statement

A developer keeps their guidelines in one repository and works in several
others (#31). Today each project needs its own copy of `stele.md`, and the
copies drift. They want each project to point at the guidelines and have its
agent and its CI follow them, without copying them in.

ADR-0002 already says rules reach stele through sources, and ADR-0003 (D55)
that rules from elsewhere arrive through a source and nothing else. What is
missing is a source that reads another repository, and how it stays out of
the hook (D45), keeps every developer and every CI run on the same rules, and
keeps a cloned repository from abusing it.

## Considered Options

* **An external source plugin** (`stele-source-git`, #14). Waits for the
  plugin protocol, and every user has to install a second program for
  something most of them want.
* **A plain HTTPS download** from github.com or gitlab.com. Needs a token
  stele would have to find and send (D65 limits tokens to those two hosts
  for publish), and works with no other host.
* **A built-in `git` source that runs `git` on `PATH`** at `stele compile`,
  `stele check` and `stele init`, never in the hook, and pins the commit in
  `stele.lock`.

## Decision Outcome

Chosen option: "a built-in `git` source", because it works with any host and
with private repositories through the user's own git login, needs nothing
installed beyond git, and fits the cold path the spec already gives external
sources.

**Configuration.** `stele.json` lists it under `sources`, in ADR-0002's shape:

```json
{"sources": [{"name": "git", "config": {"repo": "git@github.com:acme/guidelines.git", "ref": "main", "path": "stele.md"}}]}
```

`repo` is checked as `stele publish` checks its own (https, ssh or
`user@host:path`; no local path, transport command, option or password).
`ref` is a branch or tag, by default the remote's default branch. `path`
ends in `.md`, by default `stele.md`. Several sources may be listed, up to
16; their rules add up, after the project's own `stele.md`, with no
precedence between them. A shared rule cannot be switched off or overridden
in this version.

**Pinned.** `stele.lock` records, per git source, the commit its rules were
read from. `stele compile` and `stele init` use the pinned commit while the
source's `repo`, `ref` and `path` in `stele.json` match it, and resolve the
ref again when they change. `stele compile --update` moves every pin to its
ref's tip. `stele check` never moves a pin: it reads the pinned commit, fails
when `stele.lock` has no pin for a listed source, and fails as today when
the rules it reads disagree with `stele.lock` or the generated rules file. A
lock with pins is `"version": 2`; one without stays version 1.

**Ids.** The guidelines repository's own `stele.lock`, beside the shared
`stele.md` at the same commit, supplies the rules' ids (D52), so a shared
rule has the same id in every project. A rule it does not list gets an id in
the destination's lock.

**Fetching.** One bare clone per repository, in the user's cache directory,
never inside the project. git is found on `PATH` outside the project, runs
outside it, with only https and ssh allowed, no prompt, no submodules,
objects checked, and nothing checked out. A commit already in the cache is
read with no network call. stele never holds or prints a credential.

**Never on the hook's path (D45).** `plugin.Builtins()` stays the sources the
hook refreshes. The git source is never among them: the hook keeps its
rules from the signed index, as it keeps an external source's. `stele stats`
does not run it either (D70). Only `internal/gitexec` starts git for it, and
`cmd/stele/hookreach_test.go` checks that neither path reaches that package.

**What leaves the machine.** A fetch sends git's protocol request to the
`repo` listed in `stele.json`: which refs and objects it wants, which of the
guidelines repository's own commits it has, git's user agent, and the user's
credentials for that host through their own git setup. It sends nothing of
the project: no rule, no code, no log, no project id. D53 and D66 still
govern everything that sends project data.

### Positive Consequences

* Any host, private repositories included, with the login the user already
  has for git.
* Every developer and every CI run use the same rules until someone moves
  the pin on purpose, and the change shows in the `stele.lock` diff.
* The hook stays as fast and as offline as before.

### Negative Consequences

* `stele compile`, `stele check` and `stele init` can now reach the network,
  and a cloned repository's `stele.json` chooses which host: ssh will offer
  the user's keys to it. This is the same exposure as a submodule or a Go
  module path, and it is documented.
* CI needs read access to the guidelines repository.
* On a fresh clone, before the first `stele compile` or `stele init` on that
  machine, the hook's deny and check know only the project's own rules. The
  committed rules file still brings the shared ones to the agent.
* An older stele refuses a version 2 `stele.lock`.
