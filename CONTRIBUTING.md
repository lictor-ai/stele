# Contributing

Thanks for helping. Bugs and ideas go in [issues](https://github.com/lictor-ai/stele/issues).
A security problem doesn't: see [SECURITY.md](SECURITY.md).

## Build and test

stele needs Go 1.27 or later (`go.mod` pins the toolchain, 1.27.4).

```sh
go build ./...
go vet ./...
go test ./...
```

While you work, test one package:

```sh
go test ./internal/check/ -count=1
go test ./test/functional/ -short     # the black-box tests, without the 2000-file check
```

`test/functional/` builds the `stele` binary and drives it like a user would:
see its [README](test/functional/README.md). `test/functional/agent/` is the
agent eval; its tests run no agent, but `go run ./test/functional/agent` does,
and costs money ([docs/eval.md](docs/eval.md)).

CI runs `go vet`, `go test` and `go build` on Linux and Windows, so tests must
pass on both: use `t.TempDir()`, build paths with `filepath`, and compare them
after `filepath.ToSlash`.

## stele checks this repository

This repository uses stele on itself. Its rules are in [stele.md](stele.md);
read them before you change code. With stele on your `PATH`, Claude Code
gets them through the hooks in `.claude/settings.json`. CI runs the same check
on every pull request:

```sh
go run ./cmd/stele check --base origin/main
```

If you change `stele.md`, run `go run ./cmd/stele compile` and commit the
updated `stele.lock` and `.claude/rules/stele-rules.md` with it.

## Which documents decide

[CLAUDE.md](CLAUDE.md) lists the authoritative documents and their order: the
v1 spec in `docs/superpowers/specs/`, the ADRs in `docs/adr/` (ADR-0004 wins
over the spec on v1 scope), and `docs/architecture.md`. `stele-design-doc.md`
is discussion, not decisions.

A change that alters a decision updates the spec in the same pull request. A
new decision gets an ADR: copy `docs/adr/adr-template.md`, take the next free
number, add it to the list in CLAUDE.md and give it a D-number in the spec.
An accepted ADR is not rewritten: a new one amends or supersedes it and says
which sections.

## README

The README describes only what is built. Every example output in it must be
real output of the current stele: run the command and paste what it printed.
When a behaviour changes, update [Limits](README.md#limits) in the same pull
request, and never remove a limit that still holds.

## Commits and pull requests

- One issue per pull request, where you can.
- Title: a short sentence, often with the area first, and the issue number:
  `hook: ask before a write to stele's signing key (#152)`.
- Body: what changed and why, in plain words. Start with `Closes #N.` when the
  pull request finishes an issue, `Part of #N.` when it doesn't.
- Pull requests are squash-merged.

By contributing, you agree that your contribution is licensed under the
[Apache License 2.0](LICENSE).
