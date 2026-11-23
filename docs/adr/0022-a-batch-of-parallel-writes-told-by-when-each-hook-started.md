# A batch of parallel writes, told by when each hook started

* Status: accepted
* Date: 2026-11-23
* Amends: ADR-0004 "How the hook works, in detail", the "Parallel
  writes" bullet; spec D58 and D61

## Context and Problem Statement

ADR-0004 denies, "for a short window after a deny (a few seconds, shorter
than any model round trip)", every write that needs the same rules, because
the agent often issues several writes in one message and their hooks run
before it reads any deny reason. The agent reads the rules once and retries
the whole batch.

That window was 3 s, measured from the time each hook took after it got the
session lock. On a loaded machine a hook in a batch of 16 waited more than
3 s for the lock, fell outside the window, and its write went through
without the agent having read the rules (#151). The session lock wait was
2 s, so in the same run some hooks gave up on the lock and let their writes
through with a warning.

The turn (`prompt_id`, Codex's `turn_id`) does not tell a batch apart: one
turn holds many model round trips.

## Considered Options

* **A longer window, still timed from the lock.** Any length can be beaten
  by a long enough queue, and a longer window denies more writes the agent
  sent after reading the rules.
* **Group by turn.** Wrong: one turn holds many messages, and the writes of
  a later message come after the agent read the earlier deny.
* **Time each hook from when it started, before any lock wait, and keep
  the batch in the signed session state.** A hook's start says when the
  agent sent the write; how long it then queued does not move it out of
  the batch.

## Decision Outcome

Chosen option: "time each hook from when it started", because it is the
only one of the three whose grouping does not depend on how long the hooks
of a batch wait for each other.

**When a hook started.** `stele hook` takes the time after reading the
event, before it waits for the compile lock, the session lock or anything
else (`backstop.Input.Start`). `Input.Now` stays the time it decides,
holding the session lock; a deny is recorded at `Now`.

**The batch, kept in the session.** `session.State.Batch` holds, per agent
(`""` for the main agent), the batch of the agent's last deny: `Start`, its
first deny, `End`, its last, and `Chain`, the first deny of its current
chain. A chain is a run of denies each less than `RetryAfter` (0.5 s) after
the one before. The state file stays signed and written through
`session.Update`; a new `prompt_id` empties `Batch` with the rest of the
turn. An older state file loads with no batch.

**In the batch.** A write is in the agent's batch when

* it started before the batch's last deny was answered, or less than
  `RetryAfter` after it, and not more than `Lead` (10 s) before the batch's
  first deny (chained); or
* it started less than `Window` (3 s) after the batch's first deny, for a
  batch the agent starts slowly.

A rule shown between the batch's first and last deny is shown again to a
write in the batch. A deny in the batch extends it; one outside starts a new
batch. `Lead` keeps a clock moved back from chaining a hook to a batch, which
could deny a write until the clock caught up.

**A retry, counted per chain.** A write to a file whose write was denied is
the agent's retry when it started at least `RetryAfter` after that deny and
is not chained to the chain that deny is in; chained to it, it is a second
write to the file in the same message. A retry sent together with a write
that needs a new rule is chained to that write's deny, a chain begun after
the retried deny, and stays a retry.

**The shell shares the batch.** `backstop.DecideBash` lists the rules never
shown and those shown in the batch. A shell command stopped when the agent
has read every rule says it has already been given them, rather than
listing none.

**The session lock.** The hook waits for the session lock at most 5 s per
holder (`safefile.LockInTurn`, the wait starting again when the lock passes
to the next holder), and at most what is left of 8.5 s from the hook's start
in all (`session.UpdateWithin`): `compile.EnsureFresh` may already have
waited and rebuilt the index, and the agent kills a hook after the 10 s
timeout `stele init` installs, letting the write through with no word. Past
the wait the update fails and `block_on_error` decides, as before.

**The window in tests.** Only a test build (`-tags steletest`) reads
`STELE_TEST_BATCH_WINDOW`, a Go duration up to a minute, where `0` leaves
only the chain. A release build never reads it. The functional tests build
stele so.

The rest of ADR-0004 stands: rules are shown once per turn per agent, saved
before the deny, and a retry of the denied write goes through.

### Positive Consequences

* A batch of many parallel writes is denied as one, however long its hooks
  queue for the session lock.
* A write the agent sends after a model round trip is not taken into an
  earlier batch by the time its hook waited.
* The batch is in the signed session state, so it holds across hook
  processes and nothing outside stele can set it.

### Negative Consequences

* A batch is still told by timing. A write to another file that the agent
  sends within 3 s of the batch's first deny, after reading it, is denied
  once more with rules it has read; a write of the same message that starts
  more than 3 s after the batch began and more than 0.5 s after its last
  deny, such as one after a long shell command, counts the batch's rules as
  read.
* A hook waits up to 5 s for the session lock instead of 2 s, so a slow
  batch takes longer before the agent sees its answers.
