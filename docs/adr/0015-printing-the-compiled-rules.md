# stele rules: the compiled rules, for other tools

* Status: accepted
* Date: 2026-11-08
* Amends: spec §9 (D86, a new command); architecture §11 (the command
  surface)

## Context and Problem Statement

The log (ADR-0010) and `stele push` (ADR-0014) carry rule ids, paths and
counts, on purpose, and stay that way. A tool that shows those ids to a person
(a script, an editor, a sink) needs to know what each id means: the rule's
text, its scope and where it came from. Today the only way is to read the rule
files, which only a source may do (D40), or `.stele/index.json`, which is
signed, gitignored, can be stale and promises no format (its `version` is 2).

## Considered Options

* **Put the rule text in log lines or push records.** It breaks "ids, paths
  and counts only" (ADR-0010, D83), and every record would repeat the text.
* **Let tools read `.stele/index.json`.** It has no compatibility promise, is
  signed with this machine's key, and may lag behind `stele.md`.
* **A command that prints the index, brought up to date as `stele check`
  does: every source read, the git source and external plugins run.** It
  starts git and plugins, so it is slow and unsafe in a clone a tool does not
  trust, and a rule `stele.lock` does not hold yet gets a throwaway id that
  matches nothing in the log.
* **A command that prints the index, brought up to date as the hook does,
  with a versioned JSON.** The built-in sources are reread; the git source and
  external sources come from the signed index.

## Decision Outcome

Chosen option: "brought up to date as the hook does", because its ids are the
ids the hook logs, which is what a tool joins on, and it starts no process, so
an editor can run it often and offline.

**What it reads and writes** (D86). `stele rules` finds the root as every
command does. A root with neither `stele.md` nor `stele.json` has no rules: it
prints an empty list, writes nothing and makes no key. Otherwise it brings the
index up to date from the built-in sources as the hook does
(`compile.EnsureFresh`), so it may write `.stele/index.json`, `stele.lock` and
an agent's rules file, exactly what the next write in a session would. It
never writes `stele.md` or `stele.json` (D38), opens no rule file itself
(D40), starts no process and runs no plugin (D45, D79). When `stele.md` stops
parsing, the last good index is printed with a note, as the hook serves it.

**What it prints** (D86). With `--format json`, or its shorthand `--json`:

```json
{
  "v": 1,
  "project": "<stele.lock's project id>",
  "rules": [
    {
      "id": "…",
      "text": "the rule's body, as written",
      "scope": ["**/*.go"],
      "source": { "kind": "stele.md", "name": "stelemd", "file": "stele.md", "rule": 1 }
    }
  ],
  "missing": [],
  "notes": []
}
```

* `rules` are in index order: `stele.md` first, then the other sources, the
  order of the agent's rules file.
* `scope` is the rule's globs, or `null` for every file.
* `source.kind` is `stele.md`, `git` or `plugin`; `name` is the source's name
  in `stele.lock`, a stable key; `rule` is the rule's 1-based place in its
  source, as `stele compile` prints it. A `git` source also has `repo`, `ref`
  (left out for the remote's default branch), `file` and `commit`, the commit
  the text was read at.
* `missing` lists the sources `stele.lock` pins or holds rules for that the
  index lacks, such as on a fresh clone: run `stele compile`. Each is a source
  object without `rule`.
* `notes` are things the reader should know, such as the fallback above.

A field may be added within `"v": 1`; a change of meaning bumps `v`.

Without a format, or with `--format text`, it prints one line per rule (id,
source and place, scope, the rule's excerpt as `stele check` shows it) and a
count; the notes, and a line per missing source, go to stderr.

**Exits** (D86, spec §8): 0 when it printed, the empty list and the fallback
included; 2 when it could not run (a bad flag, no root, no key, an index it
could not build, a `stele.md` that does not parse with no earlier index).

It is not on the hook's path: `cmd/stele/hookreach_test.go` follows its calls
as it does those of `stele stats`.

### Positive Consequences

* A tool joins the ids of the log and of a push to each rule's text, scope and
  source, through a format with a version.
* Nothing new leaves the machine: the command prints, it sends nothing.

### Negative Consequences

* Rules from the git source and external sources are those of the last
  `stele compile`. On a fresh clone, or after a pull that moved a pin, they are
  listed under `missing` until it runs.
* It may write `.stele/`, `stele.lock` and the rules file, as the hook does.
