# Rules loaded at session start

* Status: accepted
* Date: 2026-10-25

## Context and Problem Statement

ADR-0004 delivers the rules through the hook: the first write in a turn that
needs rules the agent has not seen is denied, with those rules as the reason.
The agent eval of 2026-10-24 (74 rules, three tasks, 6 runs per arm, Sonnet
5.5) measured what that costs:

| arm | turns | tokens | followed every blocking rule |
|---|---|---|---|
| rules in CLAUDE.md | 12.3 | 308k | 6/6 |
| stele | 19.8 | 645k | 5/6 |

A stele run hit 3.3 denies on average, and Claude writes files in parallel
batches, so one deny makes it send the whole batch again. Rules already in the
context cost nothing per turn. How should stele put the rules there?

## Considered Options

* A `SessionStart` hook's `additionalContext` with the rules.
* Path-scoped rules files in `.claude/rules/` (`paths:` set to each rule's scope).
* One generated `.claude/rules/stele-rules.md` with no `paths:`, loaded at launch.
* Writing the rules into the user's `CLAUDE.md`.

## Decision Outcome

Chosen option: one generated `.claude/rules/stele-rules.md` with no `paths:`,
because it is the only one that reaches the agent before its first write
without a size cap and without writing a file the user owns. Measured with
Claude Code 2.1.310 (spec `2026-10-24-session-start-rules-design.md`):

* Claude Code caps every hook string at 10,000 characters; the eval's
  `stele.md` is 15.9 KB, so about half the rules would not fit a
  `SessionStart` answer.
* A path-scoped rules file loads only after the first write it matches, and
  never for a Bash write: the same round trip as the deny.
* A rules file with no `paths:` loads at launch, whole (16.9 KB tested), in
  every subagent, and again after a resume and a compaction.
* `CLAUDE.md` is the user's (D38's spirit).

stele writes the file from the index with `stele compile`, the hook's
recompile and `init` (D62). The hook records, at `SessionStart` and
`SubagentStart`, which rules the session loaded, and the deny shows only the
others. A session never sees a change to the file until its next resume or
compaction, and a `SessionStart` hook runs too late to refresh it, so stele
records what the file held when the session read it, never what it writes
next. Both are built (#68): the file first, then the recording.

### Positive Consequences

* The agent has every rule before its first write, as with `CLAUDE.md`, and
  each subagent loads them itself.
* No size cap, and no file of the user's is touched.
* The deny remains for a rule changed or added mid-session, and for a
  subproject's rules.

### Negative Consequences

* One more committed file. `stele check` fails until it is compiled and
  committed, which every existing repository must do once after upgrading.
* Every rule costs context on every turn, as a long `CLAUDE.md` does.
* The file is Claude Code's format; another agent needs its own adapter.
