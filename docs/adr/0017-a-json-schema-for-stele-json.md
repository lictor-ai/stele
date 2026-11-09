# A JSON Schema for stele.json

* Status: accepted
* Date: 2026-11-09

## Context and Problem Statement

`stele.json` is written by hand, and an editor cannot check it: a typo shows
up only when a stele command reads the file, and some never do. stele reads
each top-level key on its own and ignores the others, so `"block_on_eror":
true` is silently a no-op. Entries of `sources`, `checkers` and `sinks`, and
a git source's `config`, are read strictly, but their mistakes still wait for
`stele compile`, `stele check` or `stele status`.

Editors check JSON against a JSON Schema named by the file's `"$schema"`
key, or by SchemaStore's catalog for the file's name. stele has none.

## Considered Options

* **The schema in the stele repository, served from the website.** The
  address is short and on a domain the project controls; the website build
  copies the file from a stele release, so nobody copies it by hand.
* **The schema in the stele repository, served from raw GitHub.** Nothing
  to deploy, but a long address tied to the repository's name and host, and
  one that follows `main` unless every user pins a release in it.
* **Embedded in the binary, written by a command.** stele would write a
  file for the editor; stele writes neither `stele.md` nor `stele.json`
  (D38), and a written schema goes stale with the binary that wrote it.

## Decision Outcome

Chosen option: "the schema in the stele repository, served from the
website", because it gives a stable, short address without a hand copy, and
it is what comparable tools do.

* The schema is `schema/stele.v1.json`, JSON Schema draft-07, the dialect
  editors support best. Its `$id`, and the address to use, is
  `https://stele.lictorlabs.com/schema/stele.v1.json`.
* Versioned by file name, as every format is (D50). A new key is additive
  and stays in v1; only an incompatible change makes `stele.v2.json`.
* The website build fetches the schema from a pinned stele tag, checks it
  with a full validator, and fails rather than serve a stale copy.
* stele ignores `"$schema"` and never writes `stele.json` (D38). Nothing in
  stele reads the schema: it is for editors.
* The schema accepts every `stele.json` stele accepts. It is stricter in
  two places, on purpose, so the editor flags what stele ignores: an unknown
  top-level key, and an unknown key under `publish`. It cannot see what
  only stele checks: a name listed twice, a timeout outside 1s–30m, and
  every rule of a repository URL.
* `backstop` is in it, deprecated: stele no longer reads it (ADR-0008), and
  `stele init` says so.
* Tests keep it from drifting, with no new dependency (architecture §11): a
  small draft-07 validator for tests (`internal/schematest`); tables of
  files stele accepts and refuses, checked against both stele and the
  schema; reflection tests that the schema's keys are the json tags stele
  decodes, both ways, with the top-level keys in `enforce.Keys()`; and every
  `stele.json` in the README and `docs/` validated.
* Later, an entry in SchemaStore's catalog for `stele.json`, so editors
  check the file with no `"$schema"` key.

### Positive Consequences

* Editors flag mistakes as they are typed, and suggest keys with their help
  text.
* The schema cannot gain or lose a key without a test failing.

### Negative Consequences

* The address works only once the website serves it; until the website
  build that pins a release, it is a 404.
* A key added to stele reaches editors only with the next release and
  website build.
* A schema is not stele: a file the schema accepts can still be refused by
  stele, for the checks listed above.
