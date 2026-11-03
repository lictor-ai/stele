# stele stats: how the rules are doing

Status: built (#98). Date: 2026-11-03. Spec: D70, which amends D69.

## Why

stele now keeps a log of what it does (ADR-0010, D69). A developer should be
able to read it back and see, on their own machine:

- which rules are broken most, and whether the agent fixed them right after;
- which rules no write ever needed: their scope is wrong or obsolete;
- how often writes were denied, day by day;
- the same, per agent, once there is more than one.

And it must say when there is too little data to mean anything (design doc
§7, `O-LOG-VOLUME`), instead of confident numbers on six events.

## A new kind: `wrote`

The log of ADR-0010 has a line only for a write stele denied or checked. A
write to a file that only rules without a `check` key apply to, loaded at the
session's start, left no trace. Judged on that log, every such rule would
look dead.

So the post-write hook now logs every write that went through:

```json
{"v":1,"kind":"wrote","project":"…","ts":"…","session":"…","agent":"claude-code","prompt":"…","file":"internal/db/query.go","rules":["…","…"],"mac":"…"}
```

`rules` are the ids of every rule whose scope applies to the file, loaded or
not, checked or not. It is written with `--check=off` too, and before the file
is read back, so a binary or removed file still counts as written. It costs one
append per write, as `checked` does, and follows the same rules: dropped
silently on any failure, never changing the answer.

A new kind keeps `"v": 1` (ADR-0010's versioning rule): an older reader skips
it.

## The command

```
stele stats [--since 30d] [--format text|json]
```

`--since` takes `Nh`, `Nd` or `Nw` (N at least 1), a day `YYYY-MM-DD` (from its
midnight, local time), or `all`. The default is `30d`.

It reads `.stele/log.jsonl` and `log.1.jsonl` through `journal.Read`, and the
current rules through the built-in sources as `stele check` does, writing
nothing. It runs no external plugin and never uses the network. It writes no
log line about itself.

Exit 0, or 2 when it could not run (a bad flag, no project, a log it could not
read). It is not a gate, so never 1.

## Definitions

**Turn.** A session and prompt pair among the `wrote`, `denied` and `checked`
lines: a user turn in which stele saw a write. Before the first prompt a
session's lines share one turn with no prompt id.

**Broken, and fixed.** A checked write that broke rule R is one broken write
for R, however many lines broke it. stele then looks at the next `checked` line
in the log for the same session, the same agent or subagent, the same file, in
the same turn, that checked R:

- clean for R: **fixed**;
- broke R again: **again** (and that line is a broken write of its own);
- no such line: **left**.

"Fixed" means the agent's next write to that file in the turn did not break the
rule *in what it wrote*. After an edit to other lines, the old violation may
still be there; `stele check` sees the whole file.

Rules are ordered by broken writes, then by the violations `stele check` found,
then by id. A rule gone from the rules is kept, and marked.

**Dead.** A rule is *met* in the window when a logged write, deny or check
touched a file its current scope applies to, or when its id is on such a line
(its scope may have changed since). A rule not met is:

- **new** when `stele.lock` does not have it yet, or when sessions loaded rules
  in the window but none loaded this one;
- **dead** when 30 turns or more happened since a session first loaded it (or
  since the window started, when no session in it loaded anything);
- **not judged** otherwise.

**Denied per day.** Denies counted by the local day of their time, write and
shell apart, with the turns of that day. The JSON names the zone.

**Per agent.** Every number is computed per agent (`agent`, the adapter's
`Name()`), and for all together. `stele check` runs have no agent and are
counted apart.

## Too little data

Two fixed thresholds, constants rather than flags for now:

- 30 turns (`MinTurns`) before stats judges a rule dead, and before the text
  stops opening with "Too little data to judge the rules";
- 10 broken writes (`MinBroken`), and 30 turns, before a fix rate is given in
  the JSON. The text gives counts, "fixed 4 of 5", never percentages.

Below them the counts are still printed: they are what happened, not a trend.

## JSON

`stats.Report`, with `"v": 1` and the `project` of `stele.lock`:

```
v, project, since (null for all), until, tz,
log: {lines, used, unverified, other_project, newer, unknown_kind, before, future},
need: {turns, broken}, rules_judged,
all: Numbers, agents: [{agent, ...Numbers}],
check_runs: {runs, with_violations, broken: {id: count}}

Numbers: enough, sessions, turns, writes, writes_checked,
  denied: {total, write, shell}, denied_by_day: [{day, total, write, shell, turns}],
  broken: [{rule_id, excerpt, in_rules, writes, violations, blocking, fixed, again, left, in_check, fix_rate}],
  dead, not_judged, new: [{rule_id, excerpt, scope, turns_since}]
```

Lists are `[]`, never `null`.

## What is left out, and said

Lines that do not verify, lines of another project (a log copied in), lines
in a newer format, kinds this stele does not know, and lines dated after now
are skipped and counted, and the text names each count that is not zero.

When `stele.md` does not compile, stats says so on stderr and judges the last
valid index, as the hook enforces it; with no valid index, it judges no rule.
When `stele.lock` is out of date it says so; the rules it lacks are new.

## Not done

- Bash writes that went through are not logged as `wrote`: the hook sees the
  command before it runs, not what it changed.
- Writes made with stele's hooks off, or before `stele init`, are not seen.
- No `--min-turns` flag, no UTC switch.
