# A local log of what stele does

* Status: accepted
* Date: 2026-11-03
* Amends: ADR-0004 ("What moves to the team milestone": logging comes back,
  with a new record); spec §7 (the record), §8 ("writes nothing"), §2.3 (the
  files under `.stele/`); architecture §3 (`internal/journal`), §7, §11 and §13
  (journal rotation)

## Context and Problem Statement

A developer cannot see how their rules are doing: which ones an agent never
meets, which it breaks, whether it fixes them right after, how often a write is
denied. `stele stats` (#98) is to answer that, from a local record of what stele
did (#11).

The spec has a log, `.stele/log.jsonl` (§7), but its record is one entry per
MCP `fetch`, with a brief, a `call_id` and an index hash. ADR-0004 moved that
log to the team milestone with the MCP server. v1 has no `fetch`, no brief and
no `call_id`: what it does happens in the hook, at a session start, before a
write and after one, and in `stele check`. Which record does v1 write, and how
does it stay safe on the hook's path?

## Considered Options

* **The spec's record**: one entry per `fetch`. There is no `fetch` in v1, so
  this logs nothing.
* **No log until the team milestone**: `stele stats` waits too.
* **A log of the hook's and `stele check`'s events**: one line per thing stele
  did, holding rule ids, paths and counts.

## Decision Outcome

Chosen option: "a log of the hook's and `stele check`'s events", because it is
the only one that records what v1 does, and it is what `stele stats` needs.

**One line per event**, in `.stele/log.jsonl`:

| `kind` | written when | its own fields |
|---|---|---|
| `loaded` | a session or subagent starts and stele records what it loaded (ADR-0005) | `rules`: the ids it loaded from the rules file |
| `denied` | a write, or a Bash command, is denied to show rules | `prompt`, `via` (`write` or `shell`), `files`, `rules` (the ids shown), `more` (rules held back) |
| `checked` | a file is checked after a write, against at least one rule | `prompt`, `file`, `rules` (the ids checked), `violations` (`rule`, `line`, `blocking`), `more` (violations past the cap) |
| `check` | `stele check` runs | `mode`, `file_count`, `rule_count`, `problems`, `errors`, `warnings`, `skipped`, `truncated`, `exit`, `broken` (rule id → count) |

Every line starts with `v` (D50), `kind`, `project` (D51), `ts` (UTC),
`session`, `agent` and, for a subagent, `subagent`. `agent` is the adapter's
new `Name()`, such as `claude-code`, a stable id that does not change with the
wording `Words().Agent` shows; it is `""` on a `stele check` line, which no
agent's hook wrote. A clean `checked` line is written too: without it, a rule
fixed right after a violation looks the same as a file never checked again.

A list and a count never share a name across kinds, so one reader's struct can
take every line.

**Only ids, paths and counts.** No code, no rule text, no excerpt, no
message, no diff. A test fails when a field outside that list is added.

**Versioning.** A new kind or a new optional field keeps `v: 1`, and readers
skip kinds they do not know. Removing a field or changing what one means bumps
`v`.

**Signed per line (D57).** The log is read back by `stele stats`, and later by
`stele push`, and nothing under `.stele/` is covered by the repository's
`.gitignore`. Each line ends with a `mac` field: the HMAC-SHA256, made with
this machine's key for purpose `log`, of the line as written without it. The
reader strips the field and verifies the bytes that were signed; a line that
does not verify (torn by a crash, made on another machine, edited, or written
by anything but stele) is skipped and counted. The MAC covers one line, not
the file: a line deleted, repeated, moved, or copied from another project's
log on the same machine still verifies. That only loses or skews this
machine's own data, which only its user can write. Every line stays plain
JSON.

**Where it is written, and where not.** A line is written only where `.stele/`
is already a real directory; the log never creates it. The hook writes after
it has decided and saved its answer, so the answer is the same whether the
line was written or not. `stele check` loads the key without creating it
(`sign.LoadExistingKey`): on a developer's machine, where stele already runs,
the run is logged; in CI, with no key and no `.stele/`, it writes nothing,
as before. A `stele check` that cannot use the rule files, or finds none,
writes no line.

**Two writers at once.** A line is appended under the lock file
`log.jsonl.lock`, opened with `O_APPEND` and never through a link
(`safefile.Append`). A writer waits at most 100 ms for the lock; past that the
line is dropped. A log whose last line was cut short gets a newline first, so
the next line stands alone. Every failure, a panic included, is dropped
without a word: the log never slows down or breaks the hook. The reader takes
the same lock while it reads both files, so it never sees a rotation half
done or a line half written.

**Size.** Past 32 MiB the log is renamed to `log.1.jsonl`, replacing an older
one, and a new log starts. The cap is soft: on Windows the rename fails while
any process has the log open, and lines are then still appended up to
64 MiB, past which they are dropped until a rename succeeds. So at most about
96 MiB on disk. A file too large to read is skipped and counted, not a failed
read. This closes the architecture's open "journal rotation".

**It stays on the machine.** stele sends it nowhere. A sink (D53, #17) will,
when listed. `.stele/` ignores itself, so git leaves the log out unless it is
added by force (`git add -f`); then it is committed and pushed like any file,
with its session and prompt ids and file paths. `stele uninstall` removes
`.stele/`, and the log with it.

### Positive Consequences

* `stele stats` has its data: dead rules, rules broken and fixed, denies per
  day, per agent.
* A second agent's lines compare with Claude Code's: same kinds, a different
  `agent`.
* A clone cannot plant lines in a developer's stats.

### Negative Consequences

* A line can be lost: a held lock, an unwritable file, a crash. Nothing
  reports it.
* Writes let through, asks, and Bash writes run again after a deny are not
  recorded: only what stele did.
* `stele check` is no longer read-only on a developer's machine: it appends
  one line.
* A log deleted or truncated by hand is not detected.
