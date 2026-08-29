# stele v1 — Design

**Date:** 2026-08-29
**Status:** approved, ready for planning
**Depends on:** `stele-design-doc.md`

`stele-design-doc.md` is the discussion document. It carries the reasoning, the
vocabulary, and the arguments. This spec is narrower: it is the set of decisions
that define v1, and it is the authority where the two disagree.

---

## 1. What v1 is

A tool that puts the right project rules in front of a coding agent at the moment
it is about to write code.

**v1 does not infer anything.** The user writes rules in one file, in stele's
format. stele parses them, matches them to the task with a subagent, delivers the
matches, and logs what happened. Reading rules out of a `CLAUDE.md` and deriving
structure from prose is the roadmap, not the first release.

That single constraint closes five of the design doc's open questions on its own
(`O-ENTRY`, `O-GRAIN`, `O-METADATA`, `O-COMPILE-PKG`, and most of `O-RULE-LOC`),
and it removes the model from the cold path entirely.

---

## 2. Architecture

### 2.1 Hot path

```
agent tries to write a file
   └─ hook armed → BLOCKED, "call stele first"

agent    → stele.request(brief, target_path?)
         ← call_id + "dispatch the stele-matcher subagent"

agent    → dispatches the subagent
subagent → stele.corpus(call_id)          ← the whole index
subagent → returns rule IDs only, to the parent

agent    → stele.fetch(call_id, ids)      ← canonical rule text
         · log written here
         · hook DISARMS

agent writes files freely
   └─ first write into an untouched directory → hook RE-ARMS
```

Three properties this shape buys, each of which was a problem before:

**The index never enters the parent's context.** `request` returns a short
instruction, not the corpus. The subagent fetches the corpus itself. This is what
keeps D6 true under a parent-dispatched matcher.

**Rule text cannot be reworded.** The subagent returns IDs. The canonical text
reaches the parent as tool output from `fetch`. A subagent summarising a
governance artifact on its way past would be a silent correctness failure.

**The log is exact, and it is a byproduct.** `fetch` is the only way to obtain the
text, so stele necessarily observes every selection. Nothing is self-reported.
This is the same move §6 makes on compile — take what you need from a call the
agent has to make anyway, rather than asking for a favour.

`call_id` is issued at `request` and presented again at `corpus` and `fetch`. It
is how a stateless binary knows three invocations are one exchange.

### 2.2 Cold path

```
stele.md  ──  stele compile  ──►  .stele/index.json
 read only        │                stele.lock
                  └── never writes back to stele.md
```

A parse and a validate. Plain Go. No model, no splitting, no derivation.

### 2.3 Process model and shared state

Everything runs locally. One binary, two modes, both spawned by the host agent:

```
stele mcp    started at session start, lives for the session, answers
             request / corpus / fetch over stdio. "MCP server" means a
             process on a pipe, not a network service.

stele hook   started on every Write/Edit, decides, exits.
```

They are separate OS processes, so they share no memory. **All state is on disk**,
which also means `stele mcp` holds nothing between calls and is a thin dispatcher.

```
stele.md                  the rules. hand-written.        USER WRITES, stele reads
stele.json                enforcement policy, optional.   USER WRITES, stele reads
stele.lock                id ↔ rule. committed.           STELE WRITES, user reads

.stele/                   everything else generated. fully gitignored.
  index.json              compiled corpus
  log.jsonl               append-only
  state/<session>.json    current prompt_id, arm state, touched dirs
  state/current           pointer to the session that last blocked
  .gitignore              contains "*"
```

**Three tiers, and the boundary between the first two is absolute.** stele reads
`stele.md` and `stele.json` and never writes to either. `stele.lock` is stele's,
committed because an identity that dies on a fresh clone is not an identity.
Everything under `.stele/` is regenerable and ignored.

The root files are at the root because that is where the team's files live —
beside `CLAUDE.md` and `AGENTS.md`, which is the company they keep. A user looking
at their repository sees their rules, not a tool's working directory.

Two things follow. Nothing in `.stele/` is ever committed, so a single
`.gitignore` containing `*` covers the whole directory and the repository's root
`.gitignore` is never touched. And `uninstall` can never delete something a human
wrote, because it only removes files stele created.

State is keyed per session (D37), so two Claude Code sessions open on the same
repository never disarm each other's hook.

**How `request` learns which session it serves.** The hook receives Claude Code's
`session_id` on stdin — verified against the hooks documentation. The MCP process
does not; MCP's own session concept is unrelated. So two mechanisms, together:

1. **The hook deposits.** On blocking it writes its `session_id` to
   `.stele/state/current`, and `request` reads it. No agent cooperation, and the
   order is always block-then-call, so the pointer is fresh.
2. **The block message carries it.** `request` takes an optional `session_id`
   argument that wins when supplied.

Correct by default, exact when the agent cooperates. Two sessions blocking in the
same millisecond can cross on (1); (2) removes even that.

**Cleanup.** Nothing tells stele that a session ended, so session files are
deleted once they are older than 24 hours. `stele uninstall` removes the directory
outright.

---

## 3. The rule format

One file: **`stele.md`, at the repository root.** Rules are separated by `---` on
its own line.

It sits at the root, not inside `.stele/`, because the user writes it and owns it.
`.stele/` is the tool's working directory and holds nothing a human wrote.

Inside a rule block, leading `key: value` lines are metadata. Everything after the
first blank line is the rule body.

```markdown
tags: [db, security]
scope: ["**/*.go"]

Never build SQL by string concatenation. Use parameterised queries.

---

tags: [http]

Every HTTP handler must go through the standard middleware chain.

---

Rules with no metadata are just prose. This is a valid rule.
```

**Note a change from the design review.** The version presented used `---` both to
close the metadata block and to separate rules, which a parser cannot disambiguate
for a rule that has no metadata. A blank line closes the metadata; `---` separates
rules. Consequence: a rule body cannot begin with a line that looks like
`key: value`. Documented, not worked around.

**Fields.** All optional.

| Field | Meaning |
| :-- | :-- |
| `description` | What the matcher keys on. Absent means the body is used. Useful only for long rules. |
| `tags` | Free vocabulary. stele ships none and imposes none. |
| `scope` | Path globs. A hint to the matcher, not a hard filter — see `O-CORPUS`. |

**Identity lives in a lockfile, `stele.lock`.** Every rule gets a UUID. The user
never writes one and never sees one in `stele.md`.

> **stele never writes to `stele.md`. It reads it. Everything stele produces goes
> in stele's own files.**

That is the governing rule, and it is D8 in its original strict form. An earlier
draft had compile stamp `id:` lines back into the source. Withdrawn: a governance
tool that edits the governance artifact is exactly the shape §6 of the design doc
warns about, even when the edit is deterministic and cosmetic.

```
stele.md      the rules. user writes, stele only reads.
stele.lock    id ↔ rule. stele writes, user never edits. committed.
.stele/       generated. gitignored.
```

`stele.lock` is the same idea as `go.sum` or `package-lock.json`: produced by the
tool, committed so the whole team and every fresh clone share it, never
hand-edited. It holds one entry per rule — id, body hash, position.

It must be committed. In `.stele/` it would be gitignored, and an id that dies on
a fresh clone or differs between developers is not an identity.

### Matching rules to ids across an edit

Compile reconciles `stele.md` against `stele.lock` in three passes:

1. **Same hash** → same rule. Handles reordering.
2. **Leftovers, same position** → same rule. Handles rewording, which is the case
   that matters: §7 tells you a rule's wording is bad, you fix it, and the id has
   to survive or the observations that prompted the fix are orphaned.
3. **Still unmatched** → a new rule. Mint an id.

Rewording *and* reordering the same rule in one edit can mismatch. It is rare, and
compile reports every id it assigned by position rather than hash, so a wrong
guess is visible in the diff rather than silent.

**Compile validates that ids are unique on every run.** UUIDs do not collide, but
a merge can duplicate an entry. That must be a loud error, not two rules quietly
sharing a history.

### The hash, doing what a content hash is actually good at

Compile stores a normalised hash of each rule body in the index. Normalisation is
lowercase, runs of whitespace collapsed, leading and trailing whitespace and
punctuation stripped per line, lines joined with a single space.

It is used for three things, none of them identity:

1. **Staleness** — skip compile entirely when the file has not changed (D23).
2. **Incremental compile** — skip rules that have not changed.
3. **Drift detection** — when a body changes substantially, say so.

```
7f3a2b1c…  body changed substantially
           "Never build SQL by string concat…"
           156 observations may not be comparable
```

Because identity no longer depends on it, the normalisation rules can change
between releases with no consequence beyond drift reporting. Under a hash id they
would have been frozen forever — any change would have silently reset every id in
every repo.

---

## 4. Tool surface

Three MCP tools.

### `stele.request(brief, target_path?) -> { call_id, instruction }`

`brief` is the agent's declaration of what it is about to do, in its own words.
`target_path` is the file it intends to write; the hook knows it exactly on the
backstop path, and the agent names it on the tool path. Both are the `O-INPUTS`
recommended v1 set, unchanged from §4.3.

Returns a `call_id` and a short instruction to dispatch the `stele-matcher`
subagent. Triggers a compile first if the index is stale.

### `stele.corpus(call_id) -> { rules: [{ id, description, tags, scope, body }] }`

The whole index. Called by the subagent, so this lands in the subagent's context
and nowhere else. Full corpus, no pre-filter (`O-CORPUS`).

### `stele.fetch(call_id, ids) -> { rules: [...], truncated: bool }`

Canonical text for the selected ids. Writes the log entry. Disarms the hook.

**Capped at 10 rules.** Over the cap, return the first 10 **in the order the
matcher returned them** and set `truncated`. The matcher's ordering is therefore
meaningful and its prompt must say so: most relevant first. Uncapped, a bad match
dumps forty rules into the context — precisely what stele exists to prevent. Ten
is a guess to be replaced from log data (`O-BUDGET`).

**Empty selection is a first-class answer.** `ids: []` returns one line saying no
rules apply, logs it, and disarms the hook. It must stay cheap and unembarrassing
or the matcher will invent relevance to look useful (`O-EMPTY`).

---

## 5. The hook

A `PreToolUse` hook on `Write` and `Edit`.

### States

| State | Behaviour |
| :-- | :-- |
| **Armed** | Blocks the write. Tells the agent to call stele. |
| **Disarmed** | Stands aside. |

**Armed at the start of every turn**, unless `backstop = false`, in which case the
hook never arms and never blocks — it only warns. Claude Code delivers a
`prompt_id` on the hook payload, which is a turn identifier, so a turn boundary is
observable rather than inferred. A new `prompt_id` re-arms the hook and clears the
touched-directory set.

Per-turn is what §4.2 of the design doc has always specified — "a hook fires the
first time the agent writes to a file *in a turn*". An earlier draft of this spec
armed per session, which would have left every turn after the first one in a
session completely unprotected.

**Disarms on a successful `fetch`** — the only unambiguous evidence that rules
were actually delivered, and the only one an agent cannot fake by calling
`request` and wandering off.

**Re-arms within a turn on the first write into a directory not yet touched.** The
comparison is on the file's immediate parent directory, matched exactly: writing
`internal/api/v1/x.go` after `internal/api/y.go` re-arms, because `internal/api/v1`
has not been written to this turn. Ancestors do not count as touched. This is
deliberately the eager reading, and §12 lists it as the thing the logs should
confirm or narrow.

**The hook stands aside inside subagents.** The payload carries `agent_type` when
the hook fires within one, and stele's own matcher must never be blocked by
stele's own backstop.

This is `O-REARM`, and what it defends against is intent drift: a brief written
twenty edits ago covers the task as it was understood then, and a handler change
that grows into a migration needs rules the first call never asked for. A wide
refactor across ten directories will re-arm ten times; if the logs say that is
noise, the signal gets narrowed.

The wording of the block matters and is a deliverable in its own right. It must
read as *code cannot land before rules are delivered*, not as *declare your intent
this instant* — an agent that reaches a write before it understands the task
should explore further and call then. See §4.2 of the design doc.

### Failure behaviour

`stele.json`, at the repository root, optional. Absent means defaults.

```json
{"backstop": true, "block_on_error": false}
```

`backstop` — block the first write of a turn until rules are delivered.
`block_on_error` — block when the matcher itself fails.

It is committed and reviewed, like `stele.md` — the enforcement policy is a team
decision, not a personal one. `init` does not write it; a user creates it only to
change a default.

| Situation | `block_on_error = false` | `= true` |
| :-- | :-- | :-- |
| No rules file yet | allow | allow |
| Rules file empty | allow | allow |
| Index stale | allow, recompile | allow, recompile |
| Matcher failed or timed out | allow, warn | **block** |
| stele binary missing | allow | allow |

`backstop` defaults on because it is the point of the tool. `block_on_error`
defaults off because a governance tool that bricks an agent on a bad install is
uninstalled once and never reinstalled.

**The last row is not ours to decide, and the README must say so.** If the binary
is missing the hook command itself fails, and the host agent decides what happens.
Strict mode means "block when stele is running and the matcher failed." It is not
a guarantee that code never lands without rules, and it should not be sold as one.

---

## 6. Install

`stele init` writes five things. Each carries a marker so `stele uninstall`
removes exactly those and nothing else.

1. The `PreToolUse` hook, in `.claude/settings.json`
2. The MCP server registration
3. `.claude/agents/stele-matcher.md` — the subagent definition, which is where the
   matcher prompt lives
4. `stele.md` at the repository root — **only if it does not already exist**, with
   a header comment explaining the format
5. `.stele/.gitignore`, containing `*`

The fourth is the single exception to D38, and it is a creation rather than a
write: `init` scaffolds an empty rule file for a repository that has none, and
nothing in stele ever touches it again. An existing `stele.md` is never opened for
writing by any command.

The fifth implements D24. Because nothing under `.stele/` is ever committed, one
wildcard covers the whole directory — including the `.gitignore` itself — and the
repository's root `.gitignore` is never edited. Everything stele writes is then
`stele.lock`, one directory, two registration entries, and a scaffolded
`stele.md`, which is what makes `uninstall` provably clean.

`init` stamps its own version. The hook checks it and warns on a mismatch rather
than failing in some confusing way.

**Target agents.** Claude Code for v1. The spawn decision requires a subagent
facility, and §4.2 requires hooks that can block — Claude Code has both. Cursor is
the likely second target. Codex and Gemini CLI have neither and are out until the
spawn story changes for them.

---

## 7. Logging

`.stele/log.jsonl`. Append-only, local, gitignored, never leaves the machine.

One entry per `fetch`:

```json
{"call_id":"...","ts":"...","brief":"...","target_path":"...",
 "index_hash":"...",
 "selected":[{"id":"7f3a2b1c-...","excerpt":"Never build SQL by string concat..."}],
 "tokens":412,"truncated":false}
```

Rule identifiers and short text only. No source code, no diffs.

Two consequences of decisions made elsewhere shape this record.

**There is no `offered` field.** D34 gives the matcher the full corpus, so
"offered" is the whole index on every call — logging it per entry would repeat two
hundred ids a turn to record nothing. `index_hash` pins which corpus was in play,
and *offered but never selected* is then derived: in the index, absent from every
`selected`.

**Selected entries carry a text excerpt.** Ids are UUIDs, so a log read six months
later is unreadable on its own, and a rule that has since been deleted resolves to
nothing at all. The excerpt makes each record self-describing. It is bounded to a
short prefix, which keeps it inside §7's "identifiers and short text only."

Three of §7's four signals are pure counting and work from day one:
*offered but never selected*, *never offered at all*, *always selected together*.
The fourth — the accumulated briefs as a corpus of how work is described in this
repo — needs volume before it says anything.

v1 records. It does not act on any of it. Acting is `O-LOGSIG`, and it stays shut
until there is data.

---

## 8. Decisions

New decisions, continuing the design doc's D-series.

| # | Decision | Closes |
| :-- | :-- | :-- |
| D12 | The tool returns a matcher prompt; the parent dispatches the subagent. | `O-SPAWN` |
| D13 | Three calls: `request`, `corpus`, `fetch`. Rule text reaches the parent from `fetch`, as tool output. | M4 |
| D14 | A `call_id` issued at `request` ties the exchange together. | M1 |
| D15 | The hook disarms on a successful `fetch`. | M3 |
| D16 | Two flags: `backstop` (default on), `block_on_error` (default off). | M2 |
| D17 | v1: the user supplies the structure. stele infers nothing. | `O-ENTRY` |
| D18 | One file, `stele.md`, rules separated by `---`. | `O-RULE-LOC` |
| D19 | The delimiter defines the unit. No splitting. | `O-GRAIN` |
| D20 | Id is a UUID held in `stele.lock`, which stele writes and commits. Rules are matched to ids by hash, then by position. The body hash is for matching, staleness and drift — never identity itself. | `O-RULE-ID` |
| D38 | stele never modifies `stele.md` or `stele.json`. It reads them. `init` may create `stele.md` when absent; nothing writes to it after that. Everything stele produces goes in its own files. | D8 |
| D21 | All metadata is authored. Nothing is derived, so nothing is reconciled. | `O-METADATA` |
| D22 | Compile is a Go parser. No model, no work order, no skill. | `O-COMPILE-PKG` |
| D23 | Compile runs automatically when the source hash changes. | `O-COMPILE-WHEN` |
| D24 | `stele.md`, `stele.json` and `stele.lock` live at the repository root and are committed. All of `.stele/` is generated and gitignored. | `O-INDEX-VCS` |
| D25 | No starter tag vocabulary. Whatever the user writes. | `O-TAGS` |
| D26 | An empty selection is a first-class, cheap answer. | `O-EMPTY` |
| D27 | Claude Code first. Cursor second. | `O-AGENTS` |
| D28 | `init` writes five marked artifacts; `uninstall` removes exactly those. | `O-INSTALL` |
| D29 | Arm at the start of every turn, keyed on `prompt_id`. Re-arm within a turn on a write into an untouched directory. | `O-REARM` |
| D30 | `fetch` is capped at 10 rules and reports truncation. | `O-BUDGET` |
| D31 | Index is a single JSON file, regenerated freely. | M5 |
| D32 | `init` stamps a version; the hook warns on mismatch. | M6 |
| D37 | Hook and MCP mode are separate processes sharing state on disk, keyed per session. | M8 |

Already argued in the design doc's prose but never recorded in §9.1. Recorded here:

| # | Decision | Where argued |
| :-- | :-- | :-- |
| D33 | The `O-INPUTS` v1 set is brief plus target path. | §4.3, closing paragraph |
| D34 | The matcher sees the full corpus, unfiltered. | §5, `O-CORPUS` |
| D35 | `O-COST` is not stele's cost in v1. The subagent runs on the parent's model and auth; stele's own work is a file read and a parse. | consequence of D12 |

**D8 stands unamended, and an earlier draft of this spec was wrong about it.** That
draft had compile stamp `id:` lines into `stele.md`, and narrowed D8 to permit it
on the grounds that a deterministic edit from the binary is not the hazard D8
describes. The narrowing has been withdrawn from `stele-design-doc.md`. Identity
lives in `stele.lock` instead — a file stele owns outright — which needs no
exception to D8 and keeps the source file untouched.

---

## 9. Corrections applied to `stele-design-doc.md`

The audit that produced this spec found three contradictions in the source
document. All three are now fixed there. Recorded here so the reasoning survives.

**`O-ENTRY` was listed as gating the architecture, but §6 had already resolved
half of it.** `O-METADATA` states outright that "the question is no longer whether
to allow authoring." The fix separates the halves: *both authored and derived
metadata can be present* is now D36, and what remains open — whether the derived
floor is good enough to be the default — is empirical, so it moved to §9.2's
settle-with-data tier. It was never a gate, because the architecture carries both
regardless of the answer.

**D7 as written forbade an option the document itself listed.** "stele never calls
a model" was recorded as a global decision but argued only for compile, which made
the `O-SPAWN` shell-out candidate a violation on paper. D7 now reads *stele never
holds model credentials and never picks a provider; it borrows the agent the user
is already running* — which covers compile on the cold path and the matcher on the
hot path, and forbids neither candidate.

**§2 and `O-CTX-RES` disagreed.** "The hot path only ever selects — it never
processes, derives, or reorganizes" was contradicted by computing extracts at call
time, which §4.3 favours. §2 now states the invariant that was actually meant:
the hot path never touches the *corpus* except to select from it, small
deterministic work on the current task's own files is allowed, and model-assisted
or persistent work is not.

Two consequential passages were stale as a result and were also updated: §1's
claim that the entry question is unsettled, and `O-ENTRY`'s prediction that
"because we cannot require [a format], all six stay open" — v1 took exactly that
route, and the six duly collapsed. They are deferred behind an authoring
requirement, not answered in general.

---

## 10. Out of scope for v1

Inferring structure from `CLAUDE.md`, `AGENTS.md` or `.cursor/rules` ·
exit-time verification (§8.1) · the read ledger and situated context
(`O-CTX-SOURCE`, `O-CTX-RES`) · acting on log signals (`O-LOGSIG`) ·
ranking by past selection (`O-ENTRENCH`) · corpus-wide reorganization
(`O-REORG`) · team log aggregation (`O-LOG-LOC`) · agents beyond Claude Code ·
blocking a turn on a compliance failure · rules checked by running a linter.

---

## 11. Known weaknesses

**The matcher prompt sits in a file the agent can edit.**
`.claude/agents/stele-matcher.md` is written by `init` and is editable by the thing
it governs. This is the hazard §6 names about `O-COMPILE-PKG`, accepted here
because it buys a much shorter `request` response and a prompt that can be tuned
without a release. It should be stated in the README, not discovered.

**Portability was spent on `O-SPAWN`.** Parent-dispatch needs a subagent facility,
an agent that obeys an instruction returned from a tool, and subagent access to
stele. §4.4 calls tools "the portable half"; D12 spends some of that. The
compensation is D35 — the matcher costs stele nothing, because it runs on the
parent's model.

**Three cooperation points instead of one.** After the hook blocks, the agent must
call `request`, dispatch the subagent, and call `fetch`. §4.2 exists to make the
tool path trustworthy, and D12 gives the agent three places to wander off instead
of one. The hook staying armed until `fetch` is what contains this: wandering off
means the write stays blocked, so the failure is loud rather than silent.

**Position matching is a guess, and it can be wrong.** Reconciliation matches by
hash first, which is exact. The leftovers are matched by position, which is an
inference. Reword and reorder the same rule in one edit and it can pair the wrong
two. Compile reports every id assigned by position rather than hash, so the guess
is visible in the diff — but it is still a guess, and a bad one silently transfers
a rule's log history to a different rule.

**`stele.lock` conflicts on merge.** Two people adding rules on separate branches
produce conflicting entries. Same as any lockfile. Resolvable by hand or by
re-running compile, but it is friction the stamping design did not have.

**The id identifies the slot, not the meaning.** An id survives any edit to the
body, including one that replaces it with something unrelated. The log history
then attaches to a rule that no longer means what it meant. The drift report is
the mitigation, not a fix. This is the *opposite* failure to a content hash: that
was over-sensitive, treating every reword as a new rule; this is under-sensitive,
treating every rewrite as the same one. Under-sensitive is the better failure,
because it is visible in a report and the other was silent.

**Opaque ids need excerpts everywhere.** A UUID cannot be read. Every human-facing
output — the drift report, any log summary, any future `stele doctor` — must print
a text excerpt beside the id or it is unusable. This is a standing requirement on
every report stele ever writes, not a one-off, and it is the price paid for the
UUID over a readable slug.

---

## 12. Open, to settle with data

Everything here is a placeholder until the logs say otherwise. Shipping §7 first
is the point.

| ID | Placeholder | What would change it |
| :-- | :-- | :-- |
| `O-BUDGET` | Cap of 10 rules | Selection sizes in the logs |
| `O-COST` | Assumed negligible | Measured turn latency |
| `O-REARM` | New-directory signal | Re-arm frequency vs. whether the second selection differed |
| `O-CORPUS` | Full corpus | The corpus size where match quality falls off |
| `O-LOG-VOLUME` | Nothing acts on the logs | Enough turns for the counts to mean something |

One cheap piece of evidence is unassigned and worth gathering before v2 reopens
`O-ENTRY`: **count how many real repositories already carry `.cursor/rules` or
`.mdc` files.** The design doc raises this as testing its own premise. A few hours
against the GitHub search API settles with evidence what §6 currently settles with
argument.
