# stele rules: the compiled rules, for other tools

* Status: accepted
* Date: 2026-11-09
* Amends: spec §8 (exit codes for one more command); `docs/architecture.md`
  §11 (the command list)

## Context and Problem Statement

The log (ADR-0010) and `stele push` (ADR-0014) carry rule ids only. A tool
that shows those ids to a person, such as a script, an editor or a sink,
needs each rule's text, scope and source, and today the only way to get them
is to read `stele.md` and the shared rule files itself. That breaks D40: only
source plugins open rule files.

So stele needs a command that prints the compiled rules, in a form another
tool can read. Three things to settle:

* how it gets the rules: from which sources, starting what, writing what;
* what the JSON holds, and how it says the list is incomplete;
* that it stays off the hook's path.

## Considered Options

* **Plan the index as `stele check` does (`compile.Plan`).** Every source is
  read again: the git source fetches, external sources run. It writes nothing,
  so a rule `stele.lock` does not hold yet gets a new id on every run: an id a
  tool cannot keep.
* **Plan the built-ins only, as `stele stats` does (`compile.PlanLocal`).** No
  process, nothing written, but the same unstable ids for new rules.
* **Read the index as the hook does (`compile.EnsureFresh`).** The built-in
  sources are compiled again when they changed, the others are carried over
  from this machine's signed index, and new rules get their ids settled in
  `stele.lock`. No process starts. It may write stele's own files, as the hook
  does.

## Decision Outcome

Chosen option: "read the index as the hook does", because it prints exactly
the rules the agent is served, with ids that stay, and starts nothing.

`stele rules [--format text|json]`:

* **JSON** (`--format json`), format version 1 (D50):

  ```json
  {
    "v": 1,
    "project": "<the project id from stele.lock>",
    "rules": [
      {
        "id": "<uuid>",
        "source": {"name": "stelemd", "path": "stele.md"},
        "scope": ["**/*.go"],
        "body": "No panic in library code."
      }
    ],
    "notes": []
  }
  ```

  `source.name` is the source's name. The project's `stele.md` adds `path`. A
  shared file from the git source (ADR-0011) adds `repo`, `ref` and `path` as
  `stele.lock` pins them, and the `commit` the index read it at. An external
  source has its name only. `scope` is `[]` for a rule that applies to every
  file. Rules come in the index's order: the project's own first.
* **Notes.** `notes` says what the list is missing, in the words stderr uses:
  a source `stele.lock` holds rules of that this machine's index does not (a
  fresh clone before `stele compile`, a pin a pull moved), or a `stele.md`
  that no longer parses, in which case the last valid rules are listed, as the
  hook serves them. A tool that reads only stdout still knows the list is
  incomplete.
* **Text**, the default: two lines a rule, its id, source and scope, then the
  start of its text, and a count. A shared file shows as its host and path,
  `@` and the commit's first seven characters.
* **Exit codes** (spec §8): 0 printed, even with notes; 2 could not run (a bad
  flag, no repository, no key, no index to fall back on).
* With no `stele.md` and no `stele.json` it prints an empty list and writes
  nothing.
* It never writes `stele.md` or `stele.json` (D38) and opens no rule file
  (D40). It is not on the hook's path: `cmd/stele/hookreach_test.go` checks
  that `runRules` reaches no process.

### Positive Consequences

* A tool can turn the ids in the log into rules a person can read, without
  parsing a rule file.
* The ids it prints are the ones the log and `stele.lock` use.
* It works offline and starts nothing, as the hook.

### Negative Consequences

* Shared and plugin rules appear only after a `stele compile` on this machine;
  until then `stele rules` lists the project's own and says what is missing.
* A read command may write `stele.lock`, `.stele/index.json` and the rules
  file, as the hook does, and creates the machine's key on first use.
* The JSON is one more format to keep stable: a change that drops or renames a
  field is format 2.
* The rules' text leaves stele through stdout. That is a command the user runs
  on their own machine, not a sink, so D53 holds: nothing leaves the machine
  unless the user sends it.
