# No `backstop` key: the hook switches replace it

* Status: accepted
* Date: 2026-10-25
* Amends: ADR-0004 ("What changes in the spec's hook", `backstop`), ADR-0006
  (Decision Outcome, its last sentence; Negative Consequences, the first point)

## Context and Problem Statement

`backstop` in `stele.json` (D16) decides whether the hook may block. Set to
`false`, the hook denies no write to show the rules, and sends violations after
a write as context instead of a block. ADR-0006 added `stele init --remind=off`,
which also stops the deny. So there are two ways to do the same thing, one set
by hand in a file stele never writes (D38), one set by a command. Which stays?

## Considered Options

* Keep both: the switches decide what runs, `backstop` whether it may block.
* Remove `backstop`; the switches are the only way.

## Decision Outcome

Chosen option: remove `backstop`, because one way to stop the deny is easier to
explain and to support than two, and the switches are set by a command.

* `--remind=on`: the hook may deny a write to show the rules. `--remind=off`:
  it never does.
* `--check=on`: a blocking violation after a write is sent back as a block.
  `--check=off`: no check runs. There is no "check but never block": a rule
  that should only warn is written as non-blocking in `stele.md`.
* `block_on_error` stays. It applies to a part that runs, whatever else is set.
* `enforce.Load` stops reading `backstop`. stele never edits `stele.json`, so a
  file that still sets it is not changed: `stele init` notes "stele.json:
  backstop is no longer read; use stele init --remind=off", and does nothing
  else.

Spec: `docs/superpowers/specs/2026-10-25-hook-switches-design.md` (#71).

### Positive Consequences

* One switch per hook, set by `stele init`, shown on every `init` run.
* The policy in `stele.json` shrinks to `block_on_error`.

### Negative Consequences

* A project with `backstop: false` gets the deny and the post-write block back
  after it upgrades, until it runs `stele init --remind=off`. Only `init` says
  so.
* "Report violations but never block" is gone as a project-wide setting; it is
  chosen per rule instead.
