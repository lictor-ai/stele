# stele v1 — Design

**Date:** 2026-08-29
**Status:** approved, ready for planning
**Depends on:** `stele-design-doc.md`
**Extended by:** `docs/adr/0002-engine-and-extension-points.md`,
`docs/adr/0003-ready-for-a-dashboard.md`

`stele-design-doc.md` is the discussion document. It carries the reasoning, the
vocabulary, and the arguments. This spec is narrower: it is the set of decisions
that define v1, and it is the authority where the two disagree.

ADR-0002 adds the engine's extension points: sources, agent adapters and
checkers. ADR-0003 fixes the formats and ids a later org-wide dashboard needs.
This spec carries both ADRs' decisions. ADR-0001 is superseded and is not a v1
format.

---

## 1. What v1 is

> **Amended by ADR-0004 (D58).** v1 is for one developer. Rules are chosen by
> their `scope`, not by a model: a `PreToolUse` hook shows the rules for a file
> before the agent writes it, and a `PostToolUse` hook checks the write. The MCP
> server and its tools (§4), the matcher subagent, and the model-driven parts of
> §2.1 and §5 move to the "team" milestone. Where this spec and ADR-0004 disagree
> about v1, ADR-0004 wins.

A tool that puts the right project rules in front of a coding agent at the moment
it is about to write code.

It is also an open engine. Where rules come from (sources), which coding agent
runs it (agent adapters), and how code is checked in CI (checkers) are slots
that plugins fill. v1 ships one of each. See §2.4.

**v1 does not infer anything.** The user writes rules in `stele.md`, in stele's
format. stele parses them, matches them to the task with a subagent, delivers the
matches, and logs what happened. Reading rules out of a `CLAUDE.md` and deriving
structure from prose is the roadmap, not the first release.

That single constraint closes five of the design doc's open questions on its own
(`O-ENTRY`, `O-GRAIN`, `O-METADATA`, `O-COMPILE-PKG`, and most of `O-RULE-LOC`),
and it removes the model from the cold path entirely.

---

## 2. Architecture

### 2.1 Hot path

> **Team milestone, not v1** (ADR-0004, D58). In v1 the hot path is the hook
> alone: it finds the rules for the file by `scope` and denies the write with them
> as the reason; a `PostToolUse` hook checks the write.

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
stele.md ─┐
other     ├─ sources ── stele compile ──►  .stele/index.json
sources  ─┘ read only         │             stele.lock
                              └── never writes back to a rule file
```

A parse and a validate. The built-in `stele.md` source is plain Go: no model, no
splitting, no derivation. Other sources are plugins (§2.4).

**Only source plugins may open rule files** (D40). A source does two things, and
nothing else in stele may do either:

1. **Read and transform** — turn its files into rules, which compile then
   reconciles against the lock and writes into the index.
2. **Has it changed** — return a fingerprint, compared against the one the index
   recorded for that source.

Everything else — the matcher, the tools, the hook, the checkers — reads
`index.json` and never learns where a rule file is.

This is a boundary, not a convenience. It keeps the user's files out of the hot
path except for a fingerprint check, it means the index carries each source's
fingerprint without ever having to interpret it, and it makes "is the index
stale" a question with exactly one owner per source instead of a concern smeared
across whichever caller happened to need it.

### 2.3 Process model and shared state

> **Amended by ADR-0004 (D58).** v1 has no `stele mcp`. The hook runs as two
> events (`PreToolUse`, `PostToolUse`), still one process per call, sharing state
> on disk through `.stele/state/`.

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
stele.json                policy and plugins, optional.   USER WRITES, stele reads
stele.lock                id ↔ rule, project id. committed. STELE WRITES, user reads

.stele/                   everything else generated. fully gitignored.
  index.json              compiled corpus
  log.jsonl               append-only
  state/<session>.json    current prompt_id, arm state, touched dirs
  state/current           pointer to the session that last blocked
  push/<sink>.json        how far stele push got, per sink
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

### 2.4 Extension points

ADR-0002 defines three slots, and v1 ships one plugin in each. ADR-0003 adds a
fourth, sinks, with no plugin in v1.

| Slot | Does | v1 ships | How it loads |
| :-- | :-- | :-- | :-- |
| **Source** | Turns rule files into rules. | `stele.md` | Built in, or an external `stele-source-<name>` |
| **Agent adapter** | Hook, MCP registration, subagent and skills for one agent. | Claude Code | Always built in, always free |
| **Checker** | Checks code against rules in CI (§8). | `regex` | Built in, or an external `stele-checker-<name>` |
| **Sink** | Sends log entries and check reports elsewhere (ADR-0003). | none | External only, `stele-sink-<name>` |

**External plugins are executables** that speak versioned JSON over stdin and
stdout: one request in, one response out. They can be closed source and written
in any language. The protocol is in `docs/architecture.md`, `internal/plugin`.

**Only plugins listed in `stele.json` and found on `PATH` ever run** (D43).
stele never runs an executable from inside the repository, even when `PATH`
points there. `stele.json` is committed, so this is what stops a cloned
repository from running code it brought with it.

```json
{
  "backstop": true,
  "block_on_error": false,
  "sources":  [{"name": "cursor-rules"}],
  "checkers": [{"name": "semgrep", "config": {}}]
}
```

**Nothing leaves the machine unless a sink is listed** (D53). Sinks run only from
`stele push` and `stele check --push`. A sink receives briefs only with
`"share_briefs": true`; code never leaves.

**`stele status --format json`** describes the install: binary and stamped
versions, adapters and what they installed, plugins with their versions and
whether they are on `PATH`, sources with fingerprints and rule counts, index
freshness, and the project id (D54). It is what a later dashboard reads.

`sources` adds to the built-in `stele.md` source; it never replaces it. A
repository without a `stele.md` simply gets no rules from it. Built-in plugins
need no entry.

**External plugins never run on the hot path** (D45). `stele compile` and
`stele check` run them. `request` refreshes built-in sources only, and keeps the
rules each external source produced at the last compile. The hook never runs a
plugin of any kind.

---

## 3. The rule format

**`stele.md`, at the repository root, is the default source** (D18). `stele.json`
can list more (§2.4). This section describes the `stele.md` format. Every source,
whatever its own format, produces the same thing: rules with a body and metadata.

In `stele.md`, rules are separated by `---` on its own line. A `---` inside a
fenced code block (```` ``` ```` or `~~~`, as in CommonMark) is part of the rule,
so a rule can show YAML front matter. So is a `---` inside an HTML comment (D56).

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

A metadata key is lowercase letters, digits, `_` and `-`, starting with a letter,
followed by `:` and then a space or the end of the line. So a body that opens
with `Note: ...` is still body, because `Note` is not a key, and so is one that
opens with `https://...`, because no space follows the colon. A value is the rest
of the line, trimmed. For `description`, `check` and `blocking`, and for each item
of a `tags` or `scope` list, one pair of matching outer quotes is removed. Every
other key is a checker's, and keeps its value as written, quotes included:
`pattern: "net/http"` matches the text `"net/http"`, quotes included.

`[a, b]` is a list. Commas inside quotes or braces do not split an item, so
`["src/**/*.{go,ts}"]` is one glob. For `tags` and `scope`, a value without
brackets is a list of one item: `tags: db` is `tags: [db]`. Other keys keep their
value as written, brackets and quotes included.

HTML comments before a rule's metadata are skipped, and a block holding only
comments is not a rule. Text after a comment's `-->` on the same line belongs to
the rule. That is how the header comment `stele init` writes stays
out of the rules (§6) (D56).

**Bad input is an error naming the file and line**, for example
`stele.md:12: rule has no body`. These are errors:

* metadata with no body after it
* metadata not followed by a blank line
* the same key twice in one rule
* a list with no closing `]`, an empty item in a list, or a quote in a list that
  never closes
* a code block or an HTML comment that never closes
* a `scope` item that is `!` with no pattern after it, has whitespace after the
  `!`, or starts with `!!` (D59)

A missing `stele.md` is not an error. It gives no rules.

**Fields.** All optional.

| Field | Meaning |
| :-- | :-- |
| `description` | What the matcher keys on. Absent means the body is used. Useful only for long rules. |
| `tags` | Free vocabulary. stele ships none and imposes none. |
| `scope` | Path globs. In v1, the filter that decides which rules the hook shows for a file (D58); a rule with no `scope` is shown for every file. An item starting with `!` excludes (D59): `["internal/**/*.go", "!**/*_test.go"]`. For the team matcher, a hint, not a hard filter — see `O-CORPUS`. For a checker it is a filter (§8). |
| `check` | The checker that checks this rule in CI, for example `regex`. Absent means the rule is guidance only. |
| `blocking` | `true` or `false`: whether a violation fails `stele check`. Default `true` when `check` is set. |

**Unknown keys are kept, not rejected** (D46). They travel with the rule into the
index, and the rule's checker receives them. That is how checker settings work:

```markdown
tags: [security]
check: regex
pattern: (?i)(password|api_key|secret)\s*[:=]\s*"[^"]{8,}"

Never put credentials in source code. Read them from the environment.
```

`pattern` means nothing to stele. It means something to the `regex` checker. It
also means the format can grow: an `owner:` key added later breaks no file that
exists today.

The matcher and the agent never see checker keys. `corpus` and `fetch` carry
description, tags, scope and body only. An agent needs the prose, not the check.

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
hand-edited. It holds one entry per rule — id, the source the rule came from,
body hash, and position within that source — and a `project` id: a UUID minted
by the first compile that identifies this repository across an organization
(D51).

It must be committed. In `.stele/` it would be gitignored, and an id that dies on
a fresh clone or differs between developers is not an identity.

### Matching rules to ids across an edit

Compile reconciles the rules from every source against `stele.lock` in three
passes, after one step that is not a guess:

0. **The source supplied an id** → use it as-is (D52). A centrally managed rule
   must have the same id in every repository. `stele.md` never supplies one. A
   supplied id that clashes with another rule's is a loud error.
1. **Same hash** → same rule. Handles reordering, and a rule moving to another
   source.
2. **Leftovers, same source and same position** → same rule. Handles rewording, which is the case
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

> **Not in v1** (ADR-0004, D58). The MCP server and its three tools are in the
> "team" milestone. v1 delivers rules through the hook's deny reason.

Three MCP tools.

### `stele.request(brief, target_path?) -> { call_id, instruction }`

`brief` is the agent's declaration of what it is about to do, in its own words.
`target_path` is the file it intends to write; the hook knows it exactly on the
backstop path, and the agent names it on the tool path. Both are the `O-INPUTS`
recommended v1 set, unchanged from §4.3.

Returns a `call_id` and a short instruction to dispatch the `stele-matcher`
subagent. Recompiles first if a built-in source has changed since the index was
built — this is the only place staleness is checked, and the only place a
recompile happens during a session. External sources are not run here (§2.4).

### `stele.corpus(call_id) -> { rules: [{ id, description, tags, scope, body }] }`

The whole index. Called by the subagent, so this lands in the subagent's context
and nowhere else. Full corpus, no pre-filter (`O-CORPUS`). Checker keys (§3) are
left out.

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

> **Amended by ADR-0004 (D58).** In v1 the hook does not arm and wait for
> `fetch`. It denies a write that brings rules not yet shown this turn, with the
> rules as the reason, and a `PostToolUse` hook checks each write. It reads rule
> content and covers subagents. `backstop` keeps its meaning; `block_on_error`
> changes meaning (see Failure behaviour); the three-blocks circuit breaker goes.

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
deliberately the eager reading, and §13 lists it as the thing the logs should
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

> **Amended by ADR-0004 (D58).** No matcher, no exchange, no three-blocks
> counter. `block_on_error` covers stele itself failing with no good index to fall
> back on; a broken `stele.md` keeps the last good rules.

`stele.json`, at the repository root, optional. Absent means defaults.

```json
{"backstop": true, "block_on_error": false}
```

The same file lists plugins (§2.4). This section covers only the two policy keys.

`backstop` — block the first write of a turn until rules are delivered.
`block_on_error` — block when the matcher itself fails.

It is committed and reviewed, like `stele.md` — the enforcement policy is a team
decision, not a personal one. `init` does not write it; a user creates it only to
change a default.

| Situation | `block_on_error = false` | `= true` |
| :-- | :-- | :-- |
| No rules file yet | allow | allow |
| Rules file empty | allow | allow |
| A call returned an error | allow, warn | **block** |
| Exchange did not complete after 3 blocks | allow, warn | **block** |
| stele binary missing | allow | allow |

**A stale index is not on this list, and an earlier draft had it here with "allow,
recompile".** That gave the hook a job it should not have. The hook runs on every
single file write and then exits; making it parse rules and rebuild an index is
real work on the hottest path in the system. It also does not need to. The hook
never reads rule content — it decides on the turn id, the touched directories, the
recorded error and the block count. The one thing it needs from the corpus is
whether any rules exist at all, which is the first two rows above. Freshness
belongs to `request`, which is where a stale index actually matters.

**The last two rows are the same problem seen from two sides, and an earlier draft
had only the first.** The hook is a fresh process on every write and can see
nothing but the state file. So a failure it is meant to act on has to be written
down, and `stele mcp` records every error it produces.

But some failures produce no error at all. The agent reads the instruction and
never dispatches the subagent. The subagent returns nonsense and the agent never
calls `fetch`. Nothing failed, so nothing can be recorded — and all the hook sees
is an exchange that stopped partway, which is exactly what an agent ignoring the
redirect looks like. One of those must be let through and the other must be
blocked.

Hence the counter. After three blocks in a turn with no `fetch`, the failure
policy applies regardless of cause. It does not need to know *why* the exchange is
not completing, only that it is not. Without it, `block_on_error = false` is
unreachable in its most common failure mode: a broken matcher would block every
write forever, which is the exact outcome the default exists to prevent.

`backstop` defaults on because it is the point of the tool. `block_on_error`
defaults off because a governance tool that bricks an agent on a bad install is
uninstalled once and never reinstalled.

**The last row is not ours to decide, and the README must say so.** If the binary
is missing the hook command itself fails, and the host agent decides what happens.
Strict mode means "block when stele is running and the matcher failed." It is not
a guarantee that code never lands without rules, and it should not be sold as one.

---

## 6. Install

> **Amended by ADR-0004 (D58).** `init` registers two hooks, `PreToolUse` and
> `PostToolUse` on Write and Edit, and creates `stele.md` and `.stele/`. No MCP
> server registration and no subagent file.

`stele init` writes five things. Each carries a marker so `stele uninstall`
removes exactly those and nothing else. Items 1–3 are the Claude Code agent
adapter's work (§2.4); another adapter writes its own agent's equivalents.

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

> **Team milestone, not v1** (ADR-0004, D58).

`.stele/log.jsonl`. Append-only, local, gitignored. It never leaves the machine
unless the user lists a sink and runs `stele push` (§2.4).

One entry per `fetch`. `v` is the record's format version and `project` the
repository's id (D50, D51):

```json
{"v":1,"project":"...","call_id":"...","ts":"...","brief":"...","target_path":"...",
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

## 8. Checking code: `stele check`

> **Amended by ADR-0004 (D58).** In v1 the hooks, not MCP tools, put rules in
> front of the agent, and there is no log or sink: `--push` is in the "team"
> milestone. The rest of this section is v1.

The hook and the MCP tools put rules in front of the agent before it writes.
`stele check` is the other half: it checks code against those rules in CI, after
it is written. It is how rules that can be checked by a tool get checked (D47).

It does two jobs, in this order.

**1. Validate the rules.** It compiles every source, including external ones,
and writes nothing. It fails when:

- a rule file does not parse — the error names the file and the line;
- two lock entries share an id;
- `stele.lock` is out of date with the sources — the message says to run
  `stele compile` and commit the lock;
- a rule's `check` names a checker that is neither built in nor listed in
  `stele.json`;
- a checker rejects a rule's settings, such as a `regex` rule with no `pattern`
  or a pattern that does not compile.

A stale lock is an error and not a warning: CI would otherwise report violations
against ids that exist on nobody's machine.

**2. Run the checkers on the selected files.**

```
stele check                 files changed against HEAD: staged, unstaged, untracked
stele check --base <ref>    files changed between <ref> and HEAD. In CI: --base origin/main
stele check --all           every tracked file
stele check <path>...       exactly these files
```

Deleted files are skipped. A file is checked whole, not just its changed lines,
so a violation on an untouched line of a changed file is reported. That is
simpler and predictable; narrowing to changed lines can come later.

A checker receives only the rules that name it, and for each rule only the files
that match its `scope`. A rule with no `scope` gets every selected file.

**Output.** One line per violation: file, line and column, the rule id with an
excerpt (§12), the message, and whether it is blocking. `--format json` gives the
same as JSON for other tools, with `"v": 1` and the `project` id at the top
(D50). `--push` also hands that report to every listed sink.

**Exit codes.**

| Code | Meaning |
| :-- | :-- |
| 0 | Rules are valid and no blocking violation was found. Non-blocking ones are printed as warnings. |
| 1 | A rule is invalid, or a blocking violation was found. |
| 2 | stele or a plugin could not run: a missing executable, a protocol error, a crash. |

A plugin listed in `stele.json` but not installed is exit 2, never a pass. A CI
job that silently skips a check is worse than a red build. The message names the
executable it looked for.

**The `regex` checker.** Built in. It reads the rule's `pattern` as a Go regular
expression (RE2 syntax) and reports one violation per match, with the line and
column where the match starts. An optional `message` key replaces the default
message, which is the rule's excerpt.

`stele check` reads rules from the index it just planned, never from rule files
(D40). It does not touch the hook, the session state or the log. The log records
selections at coding time; it is not a CI report.

---

## 9. Decisions

New decisions, continuing the design doc's D-series.

| # | Decision | Closes |
| :-- | :-- | :-- |
| D12 | **Team milestone, not v1 (D58).** The tool returns a matcher prompt; the parent dispatches the subagent. | `O-SPAWN` |
| D13 | **Team milestone, not v1 (D58).** Three calls: `request`, `corpus`, `fetch`. Rule text reaches the parent from `fetch`, as tool output. | M4 |
| D14 | **Team milestone, not v1 (D58).** A `call_id` issued at `request` ties the exchange together. | M1 |
| D15 | **Team milestone, not v1 (D58).** The hook disarms on a successful `fetch`. | M3 |
| D16 | **Amended by D58.** Two flags: `backstop` (default on), `block_on_error` (default off). | M2 |
| D17 | v1: the user supplies the structure. stele infers nothing. Extracting rules from guideline prose comes after v1, as a paid source plugin (ADR-0002). | `O-ENTRY` |
| D18 | `stele.md`, rules separated by `---`, is the default source. `stele.json` can list more sources. *Amended by ADR-0002; was "one file".* | `O-RULE-LOC` |
| D19 | The delimiter defines the unit. No splitting. | `O-GRAIN` |
| D20 | Id is a UUID held in `stele.lock`, which stele writes and commits. Each entry records the source its rule came from. Rules are matched to ids by hash, then by position within the same source. The body hash is for matching, staleness and drift — never identity itself. | `O-RULE-ID` |
| D38 | stele never modifies `stele.md` or `stele.json`. It reads them. `init` may create `stele.md` when absent; nothing writes to it after that. Everything stele produces goes in its own files. | D8 |
| D21 | All metadata is authored. Nothing is derived, so nothing is reconciled. | `O-METADATA` |
| D22 | Compile is a Go parser. No model, no work order, no skill. Binds the built-in sources; an external source is a plugin (D42). | `O-COMPILE-PKG` |
| D23 | Compile runs automatically when the source hash changes. | `O-COMPILE-WHEN` |
| D24 | `stele.md`, `stele.json` and `stele.lock` live at the repository root and are committed. All of `.stele/` is generated and gitignored. | `O-INDEX-VCS` |
| D25 | No starter tag vocabulary. Whatever the user writes. | `O-TAGS` |
| D26 | An empty selection is a first-class, cheap answer. | `O-EMPTY` |
| D27 | Claude Code first. Cursor second. | `O-AGENTS` |
| D28 | `init` writes five marked artifacts; `uninstall` removes exactly those. | `O-INSTALL` |
| D29 | **Amended by D58.** Arm at the start of every turn, keyed on `prompt_id`. Re-arm within a turn on a write into an untouched directory. | `O-REARM` |
| D30 | **Team milestone, not v1 (D58).** `fetch` is capped at 10 rules and reports truncation. | `O-BUDGET` |
| D31 | Index is a single JSON file, regenerated freely. | M5 |
| D32 | `init` stamps a version; the hook warns on mismatch. | M6 |
| D37 | **Amended by D58.** Hook and MCP mode are separate processes sharing state on disk, keyed per session. | M8 |
| D39 | **Team milestone, not v1 (D58).** Failures are recorded in session state so the hook can see them, and a counter applies the failure policy after 3 blocks with no `fetch`. | G1, G2 |
| D40 | Only source plugins open rule files, and only to read-and-transform or to answer has-it-changed. Everything else reads `index.json`. *Amended by ADR-0002; was "only one package opens `stele.md`".* | G3 |

From ADR-0002, `docs/adr/0002-engine-and-extension-points.md`:

| # | Decision | Closes |
| :-- | :-- | :-- |
| D41 | Three extension slots: source, agent adapter, checker. v1 ships `stele.md`, Claude Code and `regex`. | — |
| D42 | Built-in plugins are compiled in. External plugins are executables, `stele-source-<name>` and `stele-checker-<name>`, speaking versioned JSON over stdin/stdout. | — |
| D43 | Only plugins listed in `stele.json` and found on `PATH` run. Never an executable inside the repository. | — |
| D44 | Agent adapters are compiled into the binary and are always free. | — |
| D45 | External plugins never run on the hot path. `request` refreshes built-in sources only. | — |
| D46 | Unknown metadata keys are kept and passed to checkers. `check` and `blocking` are the keys stele itself reads. The other keys' values are kept as written, without unquoting (#40). | — |
| D47 | `stele check` validates the rules and runs checkers on changed files, failing CI on invalid rules and blocking violations. | design doc §8.2, rules checked by running a command |
| D48 | Free if a single developer would want it. Paid only for needs that only an organization has. | — |

From ADR-0003, `docs/adr/0003-ready-for-a-dashboard.md`:

| # | Decision | Closes |
| :-- | :-- | :-- |
| D49 | A future dashboard is an ordinary client of public formats. The engine has no private API for it. | — |
| D50 | Every public format carries a version: the log entry, the `stele check` JSON report and the `stele status` JSON gain `"v": 1`. | — |
| D51 | `stele.lock` holds a `project` UUID, minted once and committed. Log entries and check reports carry it. | — |
| D52 | A source may supply a rule's id; reconciliation uses it as-is. `stele.md` never does. | — |
| D53 | A fourth slot, sinks (`stele-sink-<name>`, external only). Data leaves only through a listed sink, only from `stele push` or `stele check --push`. Briefs only with `share_briefs`. | `O-LOG-LOC`, the mechanism only |
| D54 | `stele status --format json` describes the install. `describe` reports the plugin's version. | — |
| D55 | Org-wide rules reach a repository only through a source plugin. | — |
| D56 | `stele.md` follows Markdown where it can: a `---` inside a fenced code block or an HTML comment does not separate rules, and HTML comments before a rule's metadata are skipped. | — |
| D57 | stele trusts only the generated files it wrote on this machine. `index.json` and session state carry an HMAC-SHA256 made with a per-user key kept outside every repository (the user config directory, owner-only); a file that does not verify is rebuilt or started afresh. `stele.lock` is shared on purpose and not signed. Malware running as the user is out of scope. | follows from D24 and D45: `.stele/` is not covered by the repository's `.gitignore`, so a repository could otherwise commit an index the agent is served |
| D58 | v1 is for one developer. Rules are chosen by `scope`, not by a model: a `PreToolUse` hook denies a write that brings rules not yet shown this turn, with those rules as the reason; a `PostToolUse` hook runs built-in checks on the written file and reports violations. The MCP server, matcher subagent, logging, plugins and sinks move to the "team" milestone, with D12–D15, D30, D33–D35 and D39; D16, D29 and D37 are amended. | ADR-0004 |
| D59 | A `scope` item starting with `!` excludes: a rule applies to a file that matches no exclusion and at least one other item; a scope of only exclusions applies to every other file. Order does not matter. A broken exclusion excludes nothing. | e2e 2026-10-23, What to change 1 (#39) |

Already argued in the design doc's prose but never recorded in the design doc's §9.1. Recorded here:

| # | Decision | Where argued |
| :-- | :-- | :-- |
| D33 | **Team milestone, not v1 (D58).** The `O-INPUTS` v1 set is brief plus target path. | §4.3, closing paragraph |
| D34 | **Team milestone, not v1 (D58).** The matcher sees the full corpus, unfiltered. | §5, `O-CORPUS` |
| D35 | **Team milestone, not v1 (D58).** `O-COST` is not stele's cost in v1. The subagent runs on the parent's model and auth; stele's own work is a file read and a parse. | consequence of D12 |

**D8 stands unamended, and an earlier draft of this spec was wrong about it.** That
draft had compile stamp `id:` lines into `stele.md`, and narrowed D8 to permit it
on the grounds that a deterministic edit from the binary is not the hazard D8
describes. The narrowing has been withdrawn from `stele-design-doc.md`. Identity
lives in `stele.lock` instead — a file stele owns outright — which needs no
exception to D8 and keeps the source file untouched.

---

## 10. Corrections applied to `stele-design-doc.md`

The audit that produced this spec found three contradictions in the source
document. All three are now fixed there. Recorded here so the reasoning survives.

**`O-ENTRY` was listed as gating the architecture, but §6 had already resolved
half of it.** `O-METADATA` states outright that "the question is no longer whether
to allow authoring." The fix separates the halves: *both authored and derived
metadata can be present* is now D36, and what remains open — whether the derived
floor is good enough to be the default — is empirical, so it moved to the design
doc's §9.2 settle-with-data tier. It was never a gate, because the architecture carries both
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

## 11. Out of scope for v1

Extracting rules from guideline prose, or inferring structure from `CLAUDE.md`,
`AGENTS.md` or `.cursor/rules` — post-v1, paid (ADR-0002) ·
sources other than `stele.md` and checkers other than `regex` — the slots ship
in v1, more plugins come after · paid plugins · LLM-judged checks ·
checking changed lines only · exit-time verification (design doc §8.1) · the
read ledger and situated context (`O-CTX-SOURCE`, `O-CTX-RES`) · acting on log
signals (`O-LOGSIG`) · ranking by past selection (`O-ENTRENCH`) · corpus-wide
reorganization (`O-REORG`) · team log aggregation (`O-LOG-LOC`) · agents beyond
Claude Code · blocking a turn on a compliance failure · the dashboard, and any
sink implementation — v1 ships the formats and the slot (ADR-0003), not a
destination.

**Now in scope, and an earlier draft had it here:** rules checked by running a
tool. That is what checkers are (§8). It is still not part of the hot path: a
checker runs in CI, never while the agent works.

---

## 12. Known weaknesses

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

**The `regex` checker is a blunt instrument.** It matches text, not code. It
fires inside comments and strings and misses anything split across names. It is
in v1 because it is free, has no dependencies, and covers the rules that really
are textual — secrets, banned imports, banned calls. Anything subtler belongs in
a checker plugin.

**A plugin is per machine, but `stele.json` is shared.** Listing a checker in
`stele.json` makes `stele check` fail for every teammate who has not installed
it. That is deliberate (§8), and the error says what to install, but it is still
friction a built-in does not have.

---

## 13. Open, to settle with data

Everything here is a placeholder until the logs say otherwise. Shipping §7 first
is the point.

| ID | Placeholder | What would change it |
| :-- | :-- | :-- |
| `O-BUDGET` | Cap of 10 rules | Selection sizes in the logs |
| `O-COST` | Assumed negligible | Measured turn latency |
| `O-REARM` | New-directory signal | Re-arm frequency vs. whether the second selection differed |
| D39 | **Team milestone, not v1 (D58).** Circuit breaker at 3 blocks | How often it trips, and whether real failures are caught sooner |
| `O-CORPUS` | Full corpus | The corpus size where match quality falls off |
| `O-LOG-VOLUME` | Nothing acts on the logs | Enough turns for the counts to mean something |

One cheap piece of evidence is unassigned and worth gathering before v2 reopens
`O-ENTRY`: **count how many real repositories already carry `.cursor/rules` or
`.mdc` files.** The design doc raises this as testing its own premise. A few hours
against the GitHub search API settles with evidence what §6 currently settles with
argument.
