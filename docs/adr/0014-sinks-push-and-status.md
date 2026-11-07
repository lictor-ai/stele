# Sinks, stele push and stele status

* Status: accepted
* Date: 2026-11-07
* Amends: ADR-0003 §6 ("sinks": what a push sends, how far it got, what
  `stele check --push` sends, `share_briefs`) and §7 (what `stele status`
  reports, `--offline`); ADR-0010 ("stele never sends the log": only through
  a sink `stele.json` lists, from `stele push` or `stele check --push`);
  spec §2.4, §7, §8 and D53 (made precise)

## Context and Problem Statement

ADR-0003 gave stele a fourth slot, sinks, and two commands: `stele push`, which
sends new log entries to every sink `stele.json` lists, and `stele status`,
which describes the install. ADR-0010 then built the log, ADR-0013 the plugin
protocol with its `push` call. #17 builds the two commands. Four things are
left open by the earlier decisions:

* what a push sends, and how it knows what it already sent, given that the
  log rotates (ADR-0010) and a sink may take part of a batch;
* what `stele check --push` sends, given that CI has no log and a checker's
  messages may quote code;
* what `share_briefs` does, given that v1 has no briefs;
* what `stele status` may run, and when it fails.

## Considered Options

* **Send the check report and the log as they are, track a byte offset.**
  Simple, but a byte offset means nothing once the log rotates, and the
  report carries checker messages and rule excerpts.
* **Re-encode each line into a sink record of its own.** A second format to
  version, and the sink could not tell which signed line a record came from.
* **Send each signed log line exactly as written; remember the last line
  sent by its MAC and the file it is in by its first line's MAC.** A
  rotation renames a file without changing it, so the cursor follows it.

## Decision Outcome

Chosen option: "send each signed line as written", because the log already
holds only ids, paths and counts (ADR-0010), so a sink receives nothing the log
does not, and every record carries a key a sink can drop a repeat by.

**Sinks** (D82). `stele.json` lists them under `sinks`: `name`, optional
`config`, optional `timeout`, optional `share_briefs`; names, refusals and the
cap of 16 are those of sources and checkers (D78). There is no built-in sink.
A sink is `stele-sink-<name>`, found and run as any plugin (D80). `push` sends
`{"protocol": 1, "op": "push", "config": …, "project": …, "records": […]}` and
the sink answers `{"accepted": n}`: it took the first `n` records, in order. An
`n` outside 0 to the number sent is a protocol error. A batch is at most 500
records and 4 MiB. The default time limit is 1 minute; an entry's `timeout`
sets it.

**What a push sends** (D83). Every signed line of the log, oldest first, that
this machine's key signed and that carries this project's id, exactly as
written, its `mac` included: the `mac` is the key a sink uses to drop a record
it already has. A line that does not verify, or carries another project's id,
is skipped and counted. The first push to a new sink sends the whole log:
listing the sink is the opt-in.

**How far it got** (D83). `.stele/push/<sink>.json`, sealed with this
machine's key, holds the last line sent: its MAC, the byte just past it, and
the MAC of the first line of the file it is in. A rotation renames a file
without changing it, so the cursor finds the line in `log.1.jsonl` after one
rotation; after two the line is gone, the push sends all there is and says
lines rotated out unsent. The cursor moves after each batch the sink accepted,
by what it accepted, never before. Delivery is therefore **at least once**:
stele stopped between the answer and the save sends that batch again. A cursor
that does not verify is an error, never a fresh start that would resend
everything; deleting the file is how to resend. One `stele push` runs at a
time per project (a lock under `.stele/`).

**Failures** (D83). A sink that fails, times out or answers an error stops for
this run; the others still run. There are no retries within a run: the next
push starts from the cursor. `stele push` exits 0 when every sink took
everything, or there was nothing to send or no sink; 1 when a sink took fewer
records than it was sent; 2 when stele or a sink could not run.

**`stele check --push`** (D84). After the check, where the key and `.stele/`
exist the run's own `check` line was just logged, so it pushes every pending
line as `stele push` does. In CI, with no log, it sends one record built as the
log's `check` line (counts and the rules broken), with no `mac`. It never
sends the check report: an external checker's message may quote code, and the
report carries rule excerpts. Its exit is the larger of the check's and the
push's.

**`share_briefs`** (D82) is accepted on a sink and has no effect: no log line
holds a brief. It is reserved so that a later field holding one can reach only
a sink that opted in.

**`stele status`** (D85) describes the install, as text or as JSON with
`"v": 1`: stele's version, the project id, each agent and what `init`
installed for it (hooks, switches, rules file), each listed plugin with where
it was found and the version its `describe` reports, each source with its rule
count and whether its fingerprint still matches the index's, whether the
index is fresh, and each sink with the lines waiting for it and when it was
last pushed to. It writes
nothing. It starts plugins: `describe` for each listed plugin, `fingerprint`
for each external source; `--offline` starts none and reports those as
unknown. It is never on the hook's path. It exits 0 whenever it could describe
the install, a missing or failing plugin included, which it reports; 2 when it
could not run.

Neither `stele push` nor `stele status` runs from `stele hook`, `stele stats`
or `stele mcp` (D45). stele itself opens no connection: a sink is a program the
user installs, and what it does with what it is sent is its own.

### Positive Consequences

* A sink receives exactly what the log holds, and every record has its key.
* The cursor survives a rotation and never sends a line twice in normal use.
* CI can report a run's counts to a sink without a log and without code.

### Negative Consequences

* Delivery is at least once; a sink that cares must drop repeats by `mac`.
* Lines rotated out twice between pushes are never sent.
* `share_briefs` does nothing yet.
* `stele status` starts plugins unless `--offline`.
