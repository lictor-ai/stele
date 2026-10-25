# Switching the write hooks on and off

* Status: accepted
* Date: 2026-10-25

## Context and Problem Statement

stele checks a write twice (ADR-0004). Before it, a `PreToolUse` hook denies a
write that brings rules the agent has not seen. After it, a `PostToolUse` hook
checks the lines written and sends back the rules they break. Each has a cost:
the deny costs turns (most of stele's extra cost in the agent eval of
2026-10-24, reduced but not removed by ADR-0005), and the check costs a turn
for each violation it finds.

A project should be able to turn either one off. Today only the deny can be,
through `backstop: false` in `stele.json`, set by hand: stele never writes
`stele.json` (D38). Where should the switches live, and what sets them?

## Considered Options

* A key per hook in `stele.json`, edited by hand.
* `stele init` adds or removes the hook entries in `.claude/settings.json`.
* `stele init` writes the switches as flags on the `stele hook` command in
  `.claude/settings.json`.

## Decision Outcome

Chosen option: flags on the hook command, set by `stele init --pre-write=on|off
--post-write=on|off`, because it is the only one a stele command can set
without breaking D38 and without losing the guard.

* `stele.json` is the user's: a command cannot write it.
* Removing the `PreToolUse` entry would also remove the guard that asks before
  an agent writes stele's own files and settings (#61, #65), and `init`
  restores a missing entry, so a missing entry cannot mean "off".
* stele already owns its hook entries and stamps its version on them. A
  switch that is off is one more flag there: `stele hook --installed=v1.0.0
  --pre-write=off`. The settings are committed, so the team shares the
  choice, and an agent's edit to them asks.

Both switches are on by default. `init` without a flag keeps the installed
switches, so an upgrade never turns a hook back on, and it prints them on
every run. Pre-write off skips the rule deny, for Write, Edit and Bash, and
priming; the guard and the session guidance stay. Post-write off skips the
check; the notice about invalid rules stays. The rules file is still written
(D62). `backstop` keeps its meaning: whether a part that runs may block.

Spec: `docs/superpowers/specs/2026-10-25-hook-switches-design.md` (#71).

### Positive Consequences

* A project chooses its cost with one command, and the choice is reviewed and
  shared like any other committed setting.
* Default settings do not change: a switch that is on is not written.
* The hook reads its switches from its arguments, with no extra file read.

### Negative Consequences

* Two ways to stop the deny: `--pre-write=off` and `backstop: false`. The
  README must say which does what.
* With post-write off, a violation is found only by `stele check`; with both
  off, stele enforces nothing until CI.
* An older stele run by newer settings ignores the flags and runs both hooks,
  until `init` is run again with the newer binary.
