# A sink can defer a push

* Status: accepted
* Date: 2026-11-16
* Amends: ADR-0014 "Sinks" (the push answer), "How far it got" (the
  cursor), "Failures" (the exit codes), "`stele check --push`" and
  "`stele status`"; spec D82, D83, D84 and D85

## Context and Problem Statement

ADR-0014 lets a sink answer a push in two ways: it took the first `n`
records (`{"accepted": n}`), or it failed. A sink that decides not to send
right now, for example because the user has not turned sending on in this
folder yet, has no good answer (#145):

* `accepted: n` moves the cursor past the lines and prints
  `<sink>: sent n lines`, with exit 0: the lines are lost for that sink, and
  the output says they were sent.
* `accepted: 0` is a partial push: exit 1 on every `stele push` and
  `stele check --push`, for every teammate who has not opted in.

## Considered Options

* **Keep two answers; sinks answer `accepted: n` and drop what they don't
  send.** What sinks do today. The lines are gone, and `stele status` says
  nothing waits.
* **Let a sink fail with a known error text that stele treats as a
  pause.** An error is a failure everywhere else (exit 2); matching on text
  is fragile.
* **A third answer: the sink defers the batch.** `"deferred": true` with
  `accepted: 0`, and an optional reason. The cursor stays, the push is not a
  failure, and `stele status` says why the lines wait.

## Decision Outcome

Chosen option: "a third answer", because it keeps the lines for the sink,
says honestly that nothing was sent, and is no failure for a teammate who
has not opted in. An older sink never sends the field and behaves as before.

**The answer** (amends D82). A sink may answer
`{"protocol": 1, "accepted": 0, "deferred": true, "reason": "<text>"}`: it
took none of the records and wants them all sent again on a later push.
`reason` is optional, a string, for the user; stele makes it one printable
line and keeps at most 200 bytes of it. `deferred` is valid only with
`accepted: 0`: `"deferred": true` with any other `accepted`, or with none, is
a protocol error, as is a `deferred` that is not a boolean or `null`, or a
`reason` that is not a string or `null`. `"deferred"` must be spelled in
lower case and given once, as `"protocol"` and `"error"` are. A protocol error fails that sink (exit 2), as any bad
answer does: a sink that says both "took these" and "sent nothing" is
broken, and guessing which it meant would either lose lines or send them
twice. `"deferred": false` or `null` is the same as no `deferred`, and a `reason`
without `"deferred": true` is ignored, as any other key stele does not read.

**The cursor** (amends D83). A deferral does not move the cursor. stele
stops that sink for this run, as after a partial answer, and keeps the
lines for the next push. It records the deferral, its reason and time, in
the sink's sealed cursor, `.stele/push/<sink>.json`, which a first deferral
creates; the sink's next answer that is neither an error nor a deferral
clears it (a sink that fails, crashes or times out keeps it). A deferral
after earlier batches of the same run keeps what those batches moved.

**What it says, and the exit** (amends D83). `stele push` prints
`<sink>: nothing sent (deferred: <reason>), n lines waiting`, or
`(deferred)` with no reason; after earlier batches of the run, the line
starts `<sink>: sent m lines` instead. A deferral is not a partial push:
`stele push` exits 0 when every sink took everything or deferred.

**`stele check --push`** (amends D84). Where it pushes the log, the same as
`stele push`. In CI, with no log, a deferring sink is told
`<sink>: nothing sent (deferred: <reason>); with no log here, this run's
record is not kept for later`: there is no cursor to keep it for. Either
way a deferral adds nothing to the exit, which is the check's.

**`stele status`** (amends D85). Each sink shows the lines waiting for it
and, when its last answer deferred them, the reason:
`3 lines to send (deferred: <reason>)`; in JSON, `"deferred": true` and
`"deferred_reason"`. It still starts no sink to learn this: it reads the
cursor.

### Positive Consequences

* A sink can wait for the user's consent without losing lines or failing
  the push for everyone else.
* `<sink>: sent n lines` again means only what the sink accepted.
* `stele status` says why lines wait, not just how many.

### Negative Consequences

* A sink that defers forever is called with the first batch on every push;
  the lines keep waiting, and the log may rotate them out before they are
  sent, as for a sink not run at all.
* In CI a deferred record is not sent and not kept.
* A stele from before this decision reads a deferral as `accepted: 0`: a
  partial push, exit 1, the records kept for the next push.
