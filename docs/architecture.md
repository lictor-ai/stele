# stele — Architecture and Technical Design

**Date:** 2026-08-29
**Status:** draft for review
**Implements:** `docs/superpowers/specs/2026-08-29-stele-v1-design.md`

The spec says *what* v1 does and why. This document says *how it is built* — module
layout, package boundaries, types, contracts with the host agent, error handling,
and testing.

## 0. Guidance this follows

| Source | What it settles here |
| :-- | :-- |
| [Go module layout](https://go.dev/doc/modules/layout) | `cmd/` + `internal/`. The official "server project" shape, since stele exports no library. |
| [Google Go Style — Best Practices](https://google.github.io/styleguide/go/best-practices.html) | Package naming, option structs, keeping assertions in test functions, separating CLI from logic. |
| [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | Interfaces at the consumer, concrete returns, error strings, no `panic` for normal errors. |
| [MCP Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp) | The MCP server implementation. Official, maintained with Google, spec-complete. |
| [Claude Code hooks](https://code.claude.com/docs/en/hooks) | The `PreToolUse` contract, verified rather than assumed. |

Two of these changed decisions. See §12.

---

## 1. Module layout

```
github.com/lictor-ai/stele

cmd/stele/            main, subcommand dispatch, exit codes
internal/corpus/      stele.md: parse and hash. read-only.
internal/lock/        stele.lock: identity, reconciliation
internal/index/       index.json: load, save
internal/session/     state/<session>.json: arm state, touched dirs
internal/enforce/     the enforcement policy
internal/journal/     log.jsonl
internal/matcher/     request / corpus / fetch — pure logic
internal/backstop/    the hook decision — pure logic
internal/serve/       MCP wiring over the SDK
internal/install/     init, uninstall, markers, version stamp
  assets/             embedded templates — must live here, see §9
```

`internal/` throughout. stele exports no library, so nothing lives at the module
root — the official guidance's server-project layout. Nothing is importable, which
means every package boundary can move later without breaking anyone.

No `util`, `common`, `helpers`, or `pkg`. Package names are chosen so the name and
the exported type read together: `corpus.Rule`, `session.State`, `enforce.Policy`,
`backstop.Decision`.

---

## 2. The central pattern: pure cores, IO at the edges

Every decision stele makes is a pure function of its inputs. Reading files and
writing them happens in `cmd/` and in thin adapters, never inside the logic.

```go
// internal/backstop
func Decide(in Input, st session.State, p enforce.Policy) Decision

// internal/matcher
func Select(req Request, idx index.Index) (Instruction, error)
func Fetch(sel Selection, idx index.Index, cap int) (Result, journal.Entry)
```

`Decide` is the whole backstop. No file reads, no clock, no environment. Given a
hook payload, the session state, and the policy, it returns allow or deny and why.
That makes the most behaviour-dense part of the system a table-driven test with no
fixtures, no temp directories, and no host agent.

The same shape applies to `corpus.Parse([]byte) ([]Rule, error)` and to
`lock.Reconcile`, which is the identity core.

**Two things have to be injected for this to hold.** A `call_id` needs randomness
and a journal entry needs a timestamp, so "pure" here means *pure given a clock
and an id source passed in*, not *free of them*. `Reconcile` takes `newID func()
string`; anything needing the time takes a `func() time.Time`. Real implementations
in `cmd/`, fixed stubs in tests.

This is the single most important structural choice in the document. Everything
below is consistent with it.

---

## 3. Packages

### `internal/corpus` — reading the user's file

**Read-only. This package has no write path at all** (D38). `stele.md` belongs to
the user; stele parses it and never touches it. There is deliberately no function
here that produces bytes.

```go
type Rule struct {
    Description string   // optional
    Tags        []string
    Scope       []string // path globs
    Body        string
    Position    int      // ordinal in the file, for reconciliation
    Line        int      // for error messages
}

func Parse(src []byte) ([]Rule, error)
func Hash(body string) string
```

`Rule` carries no `ID`. Identity is not a property of the source file — it is
assigned by `internal/lock`.

**`Parse`** splits on `---` at the start of a line, reads leading `key: value`
lines as metadata, and takes everything after the first blank line as the body.

**`Hash`** normalises before hashing: lowercase, runs of whitespace collapsed,
leading and trailing whitespace and punctuation stripped per line, lines joined
with a single space. It is used for reconciliation, staleness and drift — never
as identity itself (D20).

Because nothing here writes, the byte-preservation problem disappears entirely,
and with it the CRLF concern that was open in §13.

### `internal/lock` — identity

Owns `stele.lock`: committed, stele-written, never hand-edited.

```go
type Entry struct {
    ID       string `json:"id"`
    Hash     string `json:"hash"`
    Position int    `json:"position"`
}

type File struct {
    Version int     `json:"version"`
    Rules   []Entry `json:"rules"`
}

type Report struct {
    ByHash, ByPosition, Minted, Dropped int
    Guessed []string // ids matched by position — reported, because they are inferences
}

func Load(root string) (File, error)
func Save(root string, f File) error
func Validate(f File) error // ErrDuplicateID

// Reconcile is pure. newID is injected so it stays testable.
func Reconcile(rules []corpus.Rule, prev File, newID func() string) (ids []string, next File, rep Report)
```

`Reconcile` is the identity core, and it runs in three passes:

1. **Exact hash match** → same rule. Survives reordering.
2. **Remaining rules, matched by position** → same rule. Survives rewording, which
   is the case that matters, since §7 exists to prompt rewording.
3. **Still unmatched** → new rule, mint an id.

Pass 2 is an inference, so every id it assigns lands in `Report.Guessed` and gets
printed. A wrong guess moves a rule's log history to the wrong rule, and that must
be visible in a diff rather than silent.

`newID` is a parameter rather than a call to `uuid.NewString` so `Reconcile` stays
a pure function — see §2's note on injected sources.

**`Validate`** returns `ErrDuplicateID` when two entries share an id. That cannot
happen from `Reconcile`, which mints fresh UUIDs, but it can happen from a merge
that duplicated an entry. It runs on every load, because two rules silently
sharing a history is worse than a loud failure.

### `internal/index` — the build artifact

```go
type Index struct {
    Version   int
    SourceHash string
    Rules     []Entry
}

type Entry struct {
    ID, Description, Body string
    Tags, Scope           []string
    BodyHash              string
}

func Load(dir string) (Index, error)   // ErrNoIndex, ErrStale
func Save(dir string, idx Index) error // crash-safe replace, §7
```

Gitignored and regenerated freely (D24), so the format carries no compatibility
obligation. `Version` exists so a mismatch triggers a rebuild rather than a parse
error.

### `internal/session` — shared state between two processes

```go
type State struct {
    SessionID string
    PromptID  string    // the current turn
    Armed     bool
    Touched   []string  // directories written to this turn
    Blocks    int       // consecutive blocks with no fetch — the circuit breaker
    LastError string    // written by stele mcp when an exchange step fails
    UpdatedAt time.Time
}
```

One file per session, `state/<session-id>.json`, per D37.

**`LastError` and `Blocks` exist because the hook is a new process every time.**
When `request` fails, `stele mcp` knows — but the hook that runs two seconds later
started from nothing and can see only this file. With nowhere to write the failure
down, the hook cannot tell a broken matcher from an agent ignoring the redirect,
and `block_on_error = false` becomes unreachable.

`LastError` covers failures that produce an error. `Blocks` covers the ones that do
not — the agent that never dispatches the subagent, the subagent that returns
nonsense. Nothing fails in those, so nothing can be recorded; all the hook ever
sees is an exchange that did not complete.

Both reset on a successful `fetch` and at the start of a new turn.

| Field | Written by |
| :-- | :-- |
| `PromptID`, `Touched`, `Blocks` | `stele hook` |
| `Armed` | both — the hook arms, `fetch` disarms |
| `LastError` | `stele mcp` |

Two processes updating different fields of one record makes every update
read-modify-write, so state writes take a lock file for the duration — the same
treatment as `journal.Append` in §7. In practice they rarely overlap, since the
agent does one thing at a time. "Rarely" is not a concurrency design.

### `internal/enforce` — policy

```go
type Policy struct {
    Backstop     bool // default true
    BlockOnError bool // default false
}

func Load(root string) (Policy, error) // defaults when the file is absent
```

**The file is `stele.json`, at the repository root, and it is optional.**

```json
{"backstop": true, "block_on_error": false}
```

Absent means defaults, which is the normal case — `init` does not write one. A
user creates it only to change a default. That is what lets it be JSON: a file
nobody has to read does not need explanatory comments, so it costs no TOML
dependency.

It sits beside `stele.md` rather than inside `.stele/` for the same reason
`stele.md` does. The enforcement policy is a team decision, so it is committed and
reviewed. `.stele/` holds only generated files.

`Policy` is an option struct rather than functional options: both fields are
always present, both come from the same file, and neither can fail. The Google
style guide's test for choosing between them points here.

### `internal/journal` — the log

```go
type Entry struct {
    CallID     string    `json:"call_id"`
    Timestamp  time.Time `json:"ts"`
    Brief      string    `json:"brief"`
    TargetPath string    `json:"target_path,omitempty"`
    IndexHash  string    `json:"index_hash"`
    Selected   []Selected `json:"selected"`
    Tokens     int       `json:"tokens"`
    Truncated  bool      `json:"truncated"`
}

type Selected struct {
    ID      string `json:"id"`
    Excerpt string `json:"excerpt"`
}

func Append(dir string, e Entry) error
```

No `offered` field — D34 makes it the whole index every time, so `IndexHash` pins
it and *offered but never selected* is derived. Excerpts are carried because a
UUID alone is unreadable later and a deleted rule resolves to nothing (spec §7).

### `internal/backstop` — the hook decision

Pure, as in §2. One function, and its order is the whole behaviour:

```
1. agent_type is "stele-matcher"     → allow      never block our own subagent
2. !policy.Backstop                  → allow      the backstop is off
3. prompt_id differs from state      → ARM, reset Touched, Blocks, LastError
4. disarmed, dir not in Touched      → RE-ARM
5. disarmed                          → allow, record the directory
   ── armed from here ──
6. LastError is set                  → failure path
7. Blocks >= 3                       → failure path
8. otherwise                         → BLOCK, Blocks++

failure path:  policy.BlockOnError ? block, naming the error : allow, warn
```

Steps 6 and 7 are the two halves of the same problem. **6 handles failures that
produced an error** — `stele mcp` recorded it, so the hook can act on it.
**7 handles failures that produced nothing** — the exchange simply stopped, which
is indistinguishable from an agent ignoring the redirect. The counter does not
need to know which; it only needs to know the exchange is not completing.

Three is a placeholder. One would fire on the normal path, where the agent is
blocked once and complies. Two would fire on a legitimate retry. Three gives two
real attempts, and §12 of the spec lists it as a number the logs should settle.

### `internal/matcher` and `internal/serve`

`matcher` holds the three tool handlers' behaviour; `serve` holds the MCP plumbing
that calls them. Splitting them is the style guide's "separate CLI code from
library code" applied to a transport: the protocol is another client of the logic,
not the home of it.

`matcher` is also what writes `LastError` — every failure path in `request` or
`corpus` records itself before returning, because a failure the hook cannot see is
a failure the policy cannot act on.

### `internal/install`

Writes five marked artifacts and removes exactly those (D28). Every artifact
carries `stele:v<version>` in a comment or a JSON field so `uninstall` can
identify its own work and leave hand edits alone.

---

## 4. The hook contract

Verified against the Claude Code documentation rather than assumed.

**Input on stdin:**

```json
{
  "session_id": "abc123",
  "prompt_id": "550e8400-...",
  "cwd": "/home/user/my-project",
  "hook_event_name": "PreToolUse",
  "tool_name": "Write",
  "tool_input": { "file_path": "...", "content": "..." },
  "agent_id": "...", "agent_type": "..."
}
```

**Output to block:**

```json
{"hookSpecificOutput": {
   "hookEventName": "PreToolUse",
   "permissionDecision": "deny",
   "permissionDecisionReason": "<the block message>"}}
```

**Configuration** written by `init`:

```json
{"hooks": {"PreToolUse": [{
  "matcher": "Write|Edit",
  "hooks": [{"type": "command",
             "command": "stele", "args": ["hook"],
             "timeout": 10}]}]}}
```

`args` gives exec form with no shell between Claude Code and the binary.

Four things the documentation settles that the spec had to guess at:

**`prompt_id` is a turn identifier.** The spec armed the hook per *session*. The
design doc §4.2 says the backstop fires on the first write **in a turn**. `prompt_id`
makes that exact: a new `prompt_id` re-arms and clears `Touched`. See §12.

**Exit 127 does not block.** The spec claimed that a missing binary lets the write
through and that this is not ours to control. Confirmed: non-zero exit codes other
than 2 are non-blocking for `PreToolUse`. The strict-mode caveat in spec §5 is
accurate as written.

**`agent_type` identifies subagents.** The matcher subagent must never be blocked
by our own hook. `Decide` returns allow immediately when `agent_type` is
`stele-matcher`.

**Exit 2 blocks unconditionally**, overriding any JSON. stele uses the JSON path
with exit 0 so that `permissionDecision: "allow"` is a real answer, and reserves
exit 2 for nothing.

---

## 5. The MCP contract

The official SDK, `github.com/modelcontextprotocol/go-sdk/mcp` (v1.7.0).

```go
srv := mcp.NewServer(&mcp.Implementation{Name: "stele"}, nil)

mcp.AddTool(srv, &mcp.Tool{Name: "request"}, handleRequest)
mcp.AddTool(srv, &mcp.Tool{Name: "corpus"},  handleCorpus)
mcp.AddTool(srv, &mcp.Tool{Name: "fetch"},   handleFetch)

srv.Run(ctx, &mcp.StdioTransport{})
```

`AddTool` derives the input and output schemas from the Go types and validates
inputs before the handler runs, so the argument list in spec §4 is expressed once,
as a struct, and cannot drift from its schema.

**Why the SDK and not hand-rolled JSON-RPC.** I previously leaned zero-dependency.
That was wrong: this SDK is the official one, maintained in collaboration with
Google, and spec-complete. Hand-rolling means owning protocol version negotiation,
initialization, and schema generation — surface area with no upside for a tool
whose value is entirely in §5 of the design doc.

### How `request` learns which session it is serving

The hook receives `session_id`. The MCP process does not — MCP's own session
concept is unrelated to Claude Code's. Two mechanisms, together:

1. **The hook deposits.** On blocking, it writes `session_id` to
   `.stele/state/current`. `request` reads it. No agent cooperation. The order is
   always block-then-call, so the pointer is fresh.
2. **The block message carries it.** `permissionDecisionReason` names the session,
   and `request` takes an optional `session_id` argument that wins when supplied.

Correct by default, exact when the agent cooperates. Two concurrent sessions
blocking in the same millisecond can cross on (1); (2) removes even that.

---

## 6. Locating the project root

Walk up from `cwd` for a directory containing `stele.md`. If none, walk up for one
containing `.git/` and use that.

`stele.md` comes first because it is what the user placed deliberately — in a
monorepo, a subproject with its own rules should use them rather than the root's.
`.git/` is the fallback for a repository that has stele installed but no rules
written yet, which is the state right after `init`.

`.stele/` is always a sibling of `stele.md` at whichever root wins. It is never
searched for independently, because a stale `.stele/` left behind by an
`uninstall` that was interrupted should not decide where the project root is.

---

## 7. Files, atomicity and concurrency

Two processes write to `.stele/`, and stele runs on Windows as a first-class
target.

**All writes are write-temp-then-rename**, with the temp file created in the same
directory so the rename never crosses a filesystem. The guarantee this buys is
that the target is never truncated — not that the swap is atomic. See below.

stele never writes `stele.md`, so the user's rules cannot be damaged by a crash at
all. What this protects is `stele.lock` — which is committed, and losing it costs
the whole corpus its identity.

**On Windows the rename is not atomic**, which the Go documentation states
directly: *"Even within the same directory, on non-Unix platforms Rename is not an
atomic operation."* The property that still holds everywhere is the one that
matters — the target is never truncated, because the temp file holds the complete
new content until the swap. What Windows adds is a failure mode: the rename can be
refused outright when another process holds the target open, which an editor,
antivirus, or the search indexer will do. So the rename retries a few times with a
short backoff, and on final failure reports the likely cause rather than an
opaque `*LinkError`.

**`journal.Append` opens with `O_APPEND`** and writes one line per call. Short
appends are atomic on POSIX. Windows offers no equivalent guarantee, so the writer
takes a lock file for the duration of the append.

**Session state needs no locking** across sessions, because each session owns its
own file. Within a session the hook and the MCP process can write concurrently, so
both use the same crash-safe replace.

**Stale state is cleaned on write:** any `state/*.json` older than 24 hours is
removed. Nothing tells stele that a session ended.

---

## 8. Errors

Following Code Review Comments and the style guide.

**Sentinel errors for conditions callers branch on**, inspected with `errors.Is`:

```go
var (
    ErrNoIndex      = errors.New("no index")
    ErrStale        = errors.New("index is stale")
    ErrDuplicateID  = errors.New("duplicate rule id")
    ErrNoRulesFile  = errors.New("no rules file")
)
```

The failure table in spec §5 is a switch over these. That is the reason they are
sentinels rather than strings: `backstop.Decide` has to distinguish "never
compiled" from "matcher failed", and *"Do not attempt to distinguish errors based
on their string form."*

**A parse error names the line**, via `Rule.Line`, and wraps with `%w` at the end.

**Error strings are lowercase and unpunctuated.**

**No `panic` outside genuine invariant violations.** Initialization errors
propagate to `main`, which prints an actionable message and exits — not a stack
trace.

**The hook never fails loudly.** Any unexpected error inside `stele hook` is
written to stderr, which Claude Code routes to its debug log, and the process
exits 0 with an allow decision unless `BlockOnError` says otherwise. A governance
tool that crashes must not take the user's editor with it.

---

## 9. Assets

Embedded with `go:embed`:

```
internal/install/assets/
  matcher-agent.md   → .claude/agents/stele-matcher.md
  block-message.txt  → the permissionDecisionReason text
  stele-header.md    → the header comment in a new stele.md
  gitignore          → .stele/.gitignore, containing "*"
```

**They live under `internal/install/`, not at the module root.** `go:embed`
patterns resolve relative to the package directory and may not contain `..`, so a
top-level `assets/` is unreachable from the package that needs it. This is a
compiler constraint, not a preference.

Embedding rather than shipping files means `stele init` works from a single
binary. `matcher-agent.md` is written to disk by design — spec §11 records that as
a known weakness, since it lands in a file the governed agent can edit.

The block message and the matcher prompt are the two highest-leverage strings in
the project. Design doc §4.2 is explicit that a redirect reading as *call this
instant* gets obeyed literally, thin brief and all. They are versioned assets, not
string literals scattered through the code, so they can be revised and diffed.

---

## 10. Testing

| Layer | Approach |
| :-- | :-- |
| `corpus.Parse` | Table-driven. Malformed input, no metadata, empty bodies, CRLF, unicode. |
| `lock.Reconcile` | Table-driven with a stub `newID`. Reorder, reword, insert, delete, and reword-plus-reorder — the case that mismatches. |
| `backstop.Decide` | Table-driven over the full cross-product of state, policy and error. Pure, so no fixtures. |
| `matcher` | Table-driven. Cap, truncation, empty selection. |
| `index`, `session`, `journal` | Temp directories. Concurrent-write tests for the replace path, including a locked target. |
| `serve` | Drive the SDK over an in-memory transport, asserting schemas and results. |
| `cmd/stele hook` | Feed recorded stdin JSON, assert stdout JSON and exit code. |
| End to end | A real Claude Code session on this repository. |

Per the style guide, assertions stay in the `Test` function. Helpers return values
or errors and call `t.Helper()`; they do not fail tests themselves. Comparisons use
`cmp.Diff`, not a custom assertion library.

---

## 11. Command surface

```
stele init         install the five artifacts
stele uninstall    remove exactly what init wrote
stele compile      parse, reconcile ids, write stele.lock and the index
stele check        parse and reconcile in memory, write nothing (for CI)
stele mcp          run the MCP server on stdio
stele hook         read a PreToolUse payload on stdin, decide
```

Standard-library `flag` and a switch on `os.Args[1]`. Six subcommands with almost
no flags does not justify a CLI framework.

**Dependencies: two.** `github.com/modelcontextprotocol/go-sdk` and
`github.com/google/uuid`, plus `go-cmp` in tests. Config is JSON rather than TOML
specifically to avoid a third.

`mcp` and `hook` are machine-facing and never print anything friendly to stdout —
stdout is the protocol on one and the decision on the other.

---

## 12. What the research changed

**The hook arms per turn, not per session.** `prompt_id` in the hook payload is a
turn identifier. Design doc §4.2 has always said the backstop fires on the first
write *in a turn*; the spec implemented it per session because there was no way to
see a turn boundary. There is. A new `prompt_id` re-arms the hook and clears
`Touched`.

This is a correctness fix, not a refinement. Under the per-session reading, an
agent that received rules in turn one would write freely for the rest of the
session — every later turn, on any task, unprotected. Spec §5 and D29 need
updating.

**The MCP server uses the official SDK.** I had leaned toward hand-rolled
JSON-RPC for zero dependencies. The official SDK exists, is maintained with
Google, and is spec-complete; typed `AddTool` also removes a whole class of
schema-drift bug. One dependency is the right trade.

**Two spec claims are now verified rather than assumed:** a missing binary does
not block the write (exit 127 is non-blocking for `PreToolUse`), and the hook does
receive `session_id`, so §2.3's fallback is the design rather than a contingency.

**One new requirement surfaced:** `agent_type` must be checked, or the hook can
block stele's own matcher subagent.

---

## 13. Open

- **`stele check` in CI** — worth having, not yet specified. Duplicate ids should
  certainly fail. A `stele.lock` that is out of date with `stele.md`, arguably —
  that is the check that catches someone editing rules without recompiling.
- **`journal` rotation** — the log grows without bound. Not a v1 problem at one
  entry per turn, but it has no answer.

*Windows line endings were open here while compile wrote back to `stele.md`. D38
removed the write path, so nothing has to preserve them.*
