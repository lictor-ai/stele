# External plugins: protocol 1, the registry, and where they run

* Status: accepted
* Date: 2026-11-06
* Amends: ADR-0002 ("the plugin protocol": the details below, a `validate`
  call, `skipped` files, positions from order, timeouts, environment,
  working directory, stderr); ADR-0004 ("what moves to the team milestone":
  external plugins come back, as engine work); spec D42, D43 and D45 (made
  precise), D58 (plugins no longer wait for the team milestone), D71 (the
  hook's guard on generated files covers every source the lock holds),
  §2.4, §8 (how `stele compile` treats an external checker's rules) and §11
  ("checkers other than `regex`")

## Context and Problem Statement

ADR-0002 gave stele three slots and said rules and checks that are not built
in come from executables speaking JSON (D42), found on `PATH` and never inside
the repository (D43), and never on the hot path (D45). It left the details to
later, and ADR-0004 moved the work to the team milestone. #14 builds it: a
developer can then use a checker such as a linter, or rules kept somewhere
stele does not read, without stele knowing them.

Three things have to be settled for that: the protocol, how stele starts a
program it did not write without letting a cloned repository run code it
brought, and which commands may start one, given that the hook runs on every
write and `stele.json` comes with the clone.

## Considered Options

* **Go plugins or a Go API.** Ties plugins to stele's Go version and
  platform, and makes stele importable, which ADR-0002 rules out.
* **A long-running plugin process** (a server over stdin and stdout). Faster
  for many calls, but stele makes few: one `read` per compile, one `check`
  per CI run. It adds a lifecycle to get wrong.
* **One process per call, one JSON request on stdin, one JSON answer on
  stdout**, started by one package that the hook cannot reach.

## Decision Outcome

Chosen option: "one process per call", because it is what ADR-0002
described, any language can do it in a few lines, and a process that exits
after one answer leaves nothing to manage.

**Protocol 1** (D78). The executable is `stele-<kind>-<name>`, the kind
`source`, `checker` or `sink`. The name is lower-case letters, digits and
dashes, starting with a letter, at most 64; `stelemd`, `git` and `regex` are
built-in names and cannot be listed. stele writes one request, `{"protocol":
1, "op": …}`, and reads one answer, `{"protocol": 1, …}`, or `{"protocol": 1,
"error": "…"}`: exactly one JSON object, nothing after it. Fields stele does
not know are ignored, so a later plugin can add some; a missing one is a
protocol error. `describe` runs once per process before the first other
call, and must name the right kind, speak protocol 1, and give a version.

| op | sent | answered |
| :-- | :-- | :-- |
| `describe` | — | `kind`, `protocols`, `version` |
| `read` (source) | `root`, `config` | `fingerprint`, `rules`: `body` and optionally `file`, `line`, `description`, `tags`, `scope`, `meta`, `id` |
| `fingerprint` (source) | `root`, `config` | `fingerprint` |
| `validate` (checker) | `config`, `rules`: `id`, `body`, `scope`, `meta` | `problems`: `rule_id`, `message` |
| `check` (checker) | `root`, `config`, `files`, `rules` | `violations`: `rule_id`, `file`, `line`, `column`, `message`; `skipped`: `file`, `reason` |
| `push` (sink, #17) | `config`, `project`, `records` | `accepted` |

A source's rules take their position from their order in the answer; a
`position` it sends is ignored. stele sets each rule's source to the
plugin's name. A rule body may not hold a line stele uses to mark a rule in
a generated file, and an `id` must be a canonical UUID (D52).

**How a plugin runs** (D80). Only plugins listed in `stele.json` run (D43),
and only from `PATH`, looked up on `PATH` with every entry that is empty,
relative or inside the project root or the repository that holds it taken
out: a copy a clone put there is passed over, never run, and never in the
way of the real one; on Windows the current directory is not searched
either. One that leads into the project through a link is refused, and so
on Windows is anything but an `.exe` or `.com`, which would run through
`cmd.exe`. stele starts it with no arguments and no shell, in a directory
of its own outside the project (`stele/plugins` under the user's cache
directory, else the temp directory), never in the project root: on Windows
a program finds DLLs and programs it starts by bare name in its working
directory first, and a clone could bring both. `root` travels in every
request. The plugin gets the user's environment with `PATH`, and the
variables a loader or interpreter reads directories from
(`LD_LIBRARY_PATH`, the `DYLD_` paths, `PYTHONPATH`, `NODE_PATH`,
`PERL5LIB`, `PERLLIB`, `RUBYLIB`, `GEM_PATH`, `GEM_HOME`), cut the same way.
A plugin that changes into `root` and starts programs by bare name does so
at its own risk.

What a plugin starts stops with it. On Unix it runs in its own process
group, which is killed when the call ends, however it ends; on Windows it
runs in a job that kills its processes when stele closes it. A child the
plugin starts in the instant before it joins the job is not in it. A
plugin that answers but leaves a process holding its output fails the call.
`stele compile`, `stele check` and `stele init` stop on Ctrl-C or a signal
to stop, stopping the plugin, and say the call was cancelled. An answer over its cap (1 MiB for `describe`, `fingerprint`
and `validate`, 32 MiB for `read` and `check`) is a protocol error, never
cut short. Each call has a time limit: 10 seconds for `describe`,
`fingerprint` and `validate`, 1 minute for `read`, 5 for `check`; an entry's
`"timeout"` in `stele.json`, between 1 second and 30 minutes, sets `read`'s,
`check`'s and `push`'s. A plugin past it is stopped, with what it started.
The answer's `protocol` and `error` must be spelled in lower case and given
once. What a plugin writes on stderr is passed through, each line cleaned
and prefixed with its name, at most 200 lines a call; the end of it goes into
the error when it fails. A missing, refused, crashed, timed-out or wrong
plugin, or one that answers with an error, makes the command exit 2 with a
message that names the executable.

**Where plugins run** (D79). External sources run at `stele compile`, `stele
check` and `stele init`; external checkers at `stele check` only. Neither
ever runs in `stele hook` or `stele stats`. `internal/pluginexec` is the one
package that starts them; only `cmd/stele/sources.go` hands it over, and
`cmd/stele/hookreach_test.go` checks that the hook's and stats' calls never
reach it. The hook keeps an external source's rules from the signed index,
as it does a git source's (D71), and delivers them like any rule. The hook
and `stele compile` know a listed checker by its name only, so a rule naming
it is valid there and is not run; `stele check` asks the checker to
`validate` its rules, then runs it. The post-write hook runs only the
built-in checkers, as before.

**The hook's guard on generated files** (D81). D71 kept the hook from
rewriting a generated rules file from an index without a pinned source's
rules. That holds for every source: the hook never rewrites a generated
file from an index that lacks the rules of a source `stele.lock` holds rules
for, or of a pinned one.

There is no pin for an external source in protocol 1: `stele check` reads it
again, and fails if its rules changed since `stele compile`.

### Positive Consequences

* A checker or a source can be written in any language, closed or open,
  and installed like any program.
* Nothing in the hook's path can start one, by construction: the hook never
  has a runner.
* A clone cannot run a program it brought, nor point stele at one by path.

### Negative Consequences

* A plugin runs as the user, with their files, environment and network: stele
  vets where it comes from, not what it does.
* External checkers never run right after a write, only in `stele check`.
* On Windows, a process a plugin starts before it joins its job, in the
  instant after it starts, outlives it.
* One process per call costs a start each time.
