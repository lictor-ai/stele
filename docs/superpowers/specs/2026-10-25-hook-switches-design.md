# Switching the write hooks on and off

Status: built (#71). Date: 2026-10-25.

## Why

stele runs two checks around a write. Before it, a `PreToolUse` hook denies a
write that brings rules the agent has not seen, with those rules as the reason
(ADR-0004, ADR-0005). After it, a `PostToolUse` hook checks the lines the agent
wrote and sends back the rules they break. A project should be able to choose
what it pays for:

- The pre-write deny costs turns. In the agent eval of 2026-10-24 it was most
  of stele's extra cost: 3.3 denies per run, each making Claude resend its
  whole batch of writes. ADR-0005 cut it, since rules the session loaded at
  launch are no longer denied, but a project that trusts the rules file may
  not want it at all.
- The post-write check costs a turn only when it finds a violation, and the
  agent fixing the violation is what the check is for. Still, a project may
  want only CI (`stele check`) to judge the code.

Both stay on by default: the post-write check is what enforces the rules, and
the deny now fires only for rules a session has not loaded.

## Today

`backstop: false` in `stele.json` already stops the deny: the hook shows no
rules before a write, and after a write sends violations as context, never as
a block. But stele never writes `stele.json` (D38), so it is set by hand, and
nothing turns the post-write check off.

## What changes

1. `stele init` takes two flags, each `on` or `off`:
   - `--remind`: before a write, deny it to show the rules the agent has not
     seen (the pre-write deny).
   - `--check`: after a write, check the lines written against the rules
     (the post-write check).

   ADR-0006 called them `--pre-write` and `--post-write`; they were renamed
   before any code, because those names say when a hook runs, not what it
   does.
2. A switch that is off is written as a flag on the `stele hook` command in
   `.claude/settings.json`, on every stele handler:
   `stele hook --installed=v1.0.0 --remind=off`. A switch that is on is not
   written, so the settings of a project that uses the defaults do not change.
3. The hook reads the switches from its own arguments and skips the part that
   is off.
4. `init` prints the switches on every run.

## Design

### Where the switches live

On the hook command, in `.claude/settings.json`:

- stele owns its hook entries there already (`init` writes them, `uninstall`
  removes them), and stele must not write `stele.json` (D38).
- The file is committed, so the team shares the setting, as it shares the
  hooks. The guard already asks before an agent edits that file (#65).
- The hook needs no file read to know them: they arrive as arguments, like
  the version stamp.

Not by removing the hook entry: the `PreToolUse` hook also guards stele's own
files and the settings that run it (#61, #65), and `init` restores a missing
entry, so a missing entry cannot mean "off".

### `stele init`

```
stele init                      # keeps what is installed; both on in a new project
stele init --remind=off
stele init --check=off --remind=on
```

- A flag that is not given keeps the switch as the installed handlers have
  it. So running `init` again, to upgrade, never switches a hook back on.
- A value other than `on` or `off` is a usage error; nothing is written.
- Every run, changed or not, prints one line: `hooks: remind on, check
  off`.
- A change of switches only rewrites the handlers, with the same version stamp
  rules as today (a build from a commit never replaces a release's stamp).
- `uninstall` is unchanged: it removes the handlers whatever their flags.

### `internal/adapter/claudecode`

- `Switches` holds the two switches; `Switches{}` with both on is the default.
- `command(version, sw)` writes `stele hook --installed=<version>` then
  `--remind=off` and/or `--check=off`, in that order.
- `handler.stamp()` accepts the version word followed by those flags, and
  nothing else, and also returns the switches. A handler with any other word
  is still "written by hand".
- `Install(settings, version, sw)`: a handler is up to date when its version
  and its switches both match. `Installed(settings)` returns the switches too,
  so `init` can keep them.
- `OtherSteleHook` compares against `command(version, sw)`.
- `HookSwitches(args)`, next to `InstalledVersion(args)`, reads them in the hook
  (a function named `Switches` would clash with the type).
  An unknown flag is ignored, so an older binary run by newer settings keeps
  both on; the version warning already tells the user to run `init` again.

### The hook

With `--remind=off` (no deny to show the rules):

- `PreToolUse` on Write, Edit and MultiEdit: the guard for stele's own files
  and for the settings still asks (`guarded`, `loadsSettings`), and a write
  outside any project still asks where it does today (`askOutside`). The rule
  deny (`backstop.Decide`) is skipped.
- `PreToolUse` on Bash: a command that writes stele's own files still asks.
  The rule deny (`backstop.DecideBash`) is skipped.
- `SessionStart` and `SubagentStart` do exactly what they do today, priming
  included. `prime` also refreshes the index, rewrites a stale rules file for
  the next session and records the launch root; and a record of loaded rules
  costs nothing when no deny reads it, and is right if `--remind` is switched
  back on.
- The version warning still comes before a write.

With `--check=off` (no check after a write):

- `PostToolUse` still runs `freshIndex`, then skips only the check. The
  recompile is what refreshes the index and `.claude/rules/stele-rules.md`
  after the agent edits `stele.md` (a write to `stele.md` asks before it, so
  `beforeWrite` never compiles it). `invalidRules` reads that fresh index, and
  the parse-error note from `freshIndex` ("the last valid rules still apply")
  is still sent. Without them, a broken rule would go unnoticed until CI.

Delivery does not change: `stele compile`, the hook's recompile and `init`
still write `.claude/rules/stele-rules.md` (D62), whatever the switches. With
both switches off, stele still delivers the rules, keeps the rules file
fresh and guards its own files; it enforces nothing until `stele check`.

### An older stele

An older binary's `stamp()` takes exactly three words, so a handler with a
switch flag looks written by hand to it. Its `init` then keeps that handler
and adds its own, and the hooks run twice, the older set with both switches
on; its `uninstall` leaves the flagged handlers. That cannot be changed in
released binaries. So the version warning must not send an older binary to
`init`: when the running stele is older than the stamp, the warning says to
upgrade stele, not to run `stele init`. Only a binary older than the stamp
gets that wording; a newer one keeps today's.

### No more `backstop`

`backstop` goes (ADR-0008): the switches replace it.

- `enforce.Policy` loses `Backstop`; `enforce.Load` reads only
  `block_on_error`. Every `Backstop` test goes, in `cmd/stele`
  (`hook.go`, `hook_bash.go`, `failure`) and in `internal/backstop` (`Decide`,
  `DecideBash`), and `docs/architecture.md` loses its step 1: the deny
  runs when `--remind` is on, and a blocking violation after a write blocks
  when `--check` is on.
- `block_on_error` applies whenever the part that failed runs.
- `stele init` reads `stele.json`; if it sets `backstop`, `init` notes
  "stele.json: backstop is no longer read; use stele init --remind=off". It
  never edits the file (D38).
- README: remove the `backstop` row, say what `--remind` and `--check` do, and
  drop "Only applies when `backstop` is `true`" from `block_on_error`.

## Rules this must keep

- D38: stele writes neither `stele.md` nor `stele.json`.
- D45: nothing external runs in the hook.
- The hook always exits 0 and never panics out.
- The guard for stele's own files and settings runs whatever the switches.

## Spec and ADR

ADR-0006, "Switching the write hooks on and off", and D63 in the v1 spec;
ADR-0008, "No `backstop` key", and D67.
Update `docs/architecture.md` (the hook command, `claudecode.Switches`) and
the README (`stele init`, `stele.json`) with the code.

## Tests

Unit (`internal/adapter/claudecode`):

- `command` and `stamp` round-trip every combination; a handler with an extra
  or unknown word is not stamped;
- `Install` with the same switches is unchanged, with other switches
  rewrites; `Installed` returns them;
- `HookSwitches(args)`: none, one, both, unknown flags ignored.

`cmd/stele`:

- `init` with no flag keeps the installed switches; `--remind=off` then
  `init` keeps it off; a bad value writes nothing; the state line is printed;
- `--remind=off`: a write that needs unseen rules is not denied; a write to
  `stele.md` or `.claude/settings.json` still asks; a Bash write to a scoped
  file is not denied; `SessionStart` answers the guidance and primes as today;
- `--check=off`: a write with a blocking violation gets no block; after an
  edit that adds an invalid but parseable rule to `stele.md`, the index and
  rules file are refreshed and the notice is sent; after one that makes it not
  parse, the "last valid rules still apply" note is sent and nothing is
  refreshed;
- the version warning tells a binary older than the stamp to upgrade, not to
  run `init`;
- both on: today's tests pass, less the `backstop: false` cases, which go;
- `enforce.Load` ignores `backstop`; `init` notes a `stele.json` that sets it
  and leaves the file byte for byte.

Functional (`test/functional`): `install_test.go` for the settings written by
each flag; `hook_pre_test.go` and `hook_post_test.go` for one write with each
switch off.
