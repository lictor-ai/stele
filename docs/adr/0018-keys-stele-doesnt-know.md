# Keys stele doesn't know

* Status: accepted
* Date: 2026-11-09

## Context and Problem Statement

stele reads each top-level key of `stele.json` on its own and skips the
others, so `"block_on_eror": true` does nothing and nobody is told. Under
`publish`, an unknown key is skipped too, and the default fills its place:
`"pth": "team/a.md"` publishes to `stele.md`. The schema (ADR-0017) flags
both, but only in an editor that uses it, never in a terminal or in CI.
Entries of `sources`, `checkers` and `sinks`, and a git source's `config`,
already refuse a key they don't have.

A top-level key stele doesn't know may also be `"$schema"`, or a key a
newer stele reads: a project shared between developers on different
versions must keep working.

## Considered Options

* **A note for an unknown top-level key, from `stele init` and `stele
  status`; an error for an unknown key under `publish`.**
* **An error for an unknown top-level key.** Catches every typo, but a key
  from a newer stele breaks every older one, the hook included.
* **A note everywhere, the hook included.** The hook runs on every write and
  answers the agent, not the developer: a note there is noise in the
  agent's context.
* **A note for an unknown key under `publish` too.** Not breaking, but a
  typo in `path` still publishes to the wrong place.

## Decision Outcome

Chosen option: "a note for an unknown top-level key, an error under
`publish`", because the two commands a developer runs to set up and inspect
stele say what they skip, and `stele publish`, which sends the rules to
another repository, never guesses.

* `stele init` and `stele status` print one note per top-level key stele
  doesn't know: `stele.json: "block_on_eror" is not a key stele knows; it
  is ignored`. A note, never an error, and never a change to `stele.json`
  (D38). `stele status --format json` has it in `notes`.
* Known keys are `enforce.Keys()` and `"$schema"`. A key in another case,
  such as `Sources`, is known: `encoding/json` reads it.
* Every other command skips an unknown top-level key without a word, as
  before.
* `stele publish` refuses a key under `publish` other than `repo` and
  `path`, as `sources`, `checkers` and `sinks` entries do: `bad publish
  setting in stele.json: "publish" has no key "pth"; it takes "repo" and
  "path"`. A small breaking change: only a file that already holds a
  mistake stops working.
* The schema's tests take the top-level keys from `enforce.KeySchema` and
  `enforce.Keys()`, the list the notes use, so the schema and the notes
  agree.

This amends ADR-0017, "Decision Outcome": stele no longer ignores an unknown
key under `publish`, and notes one at the top level. The schema is still
stricter than stele for a key in another case and for odd URL forms.

### Positive Consequences

* A typo in a top-level key shows up the first time `stele init` or `stele
  status` runs, with no editor setup.
* `stele publish` can't send the rules to a default path because of a typo.

### Negative Consequences

* A key from a newer stele is noted by an older one, until it is updated.
* `stele compile`, `stele check` and the hook still say nothing: in CI, a
  typo shows up only if the pipeline runs `stele status`.
* A `stele.json` with an unknown key under `publish` that worked before is
  now refused by `stele publish`.
