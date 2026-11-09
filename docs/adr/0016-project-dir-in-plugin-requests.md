# project_dir in every plugin request

* Status: accepted
* Date: 2026-11-09
* Amends: ADR-0013 ("The protocol": the fields of each request; "`root`
  travels in every request", which `validate` never had); ADR-0014 (the
  `push` request, and what a sink receives); spec D53, D78 and D82

## Context and Problem Statement

A plugin sometimes needs to look at the project it works for. A sink that
also sends the project's rules, for example, has to run `stele` in the
project's folder.

Plugins run in a directory of stele's own, never the project (D80). `read`,
`fingerprint` and `check` send the project as `root`; `validate` and `push`
send no folder at all. So a sink has to guess: its working directory, which
is never the project, a CI variable such as `$GITHUB_WORKSPACE`, or a folder
it saved earlier. With two clones or worktrees of one project the guess can
pick the wrong one, and the project id cannot tell them apart: it is in the
committed `stele.lock`, so both clones share it.

## Considered Options

* **`project_dir` in every request but `describe`, `root` kept.** Additive:
  plugins ignore fields they don't know, so protocol 1 stays.
* **`root` added to `push` and `validate`.** Also additive, but `root` reads
  as "the root of something" to someone meeting the protocol for the first
  time.
* **Rename `root` to `project_dir`.** One name, but it breaks every plugin
  that reads `root`: protocol 2.
* **Run plugins in the project's folder.** Refused by D80: on Windows a
  program finds DLLs and programs it starts by bare name in its working
  directory first, and a clone could bring both.
* **An environment variable, `STELE_PROJECT_DIR`.** The request is the
  contract; an environment variable is inherited by everything the plugin
  starts, and is not in the protocol's documents.

## Decision Outcome

Chosen option: "`project_dir` in every request but `describe`, `root`
kept", because it is the only one that tells every plugin the project
without breaking one.

* Every request but `describe` carries `project_dir`: the project's root,
  absolute, as this machine writes paths (backslashes and a drive letter on
  Windows). `describe` is about the plugin, not a project.
* `read`, `fingerprint` and `check` also carry `root`, with the same value,
  as before. The protocol document calls it the old name; dropping it is a
  protocol 2 change.
* `validate` and `push` gain `project_dir` and no `root`.
* A sink now receives, besides its records, where the project is on this
  machine, for its own use. The protocol document says it can name the
  user, and that a sink should not send it on unless it means to.

### Positive Consequences

* A plugin finds the project without guessing, from any of its calls, even
  with two clones of one project.
* One name in every op; existing plugins keep working.

### Negative Consequences

* Two fields with one value until protocol 2.
* What a sink receives grows by one local path, which can hold the user's
  name (`/home/alice/…`, `C:\Users\alice\…`). It goes only to a sink the
  project lists, from `stele push` or `stele check --push` (D53), as before.
