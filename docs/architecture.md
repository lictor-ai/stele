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
internal/corpus/      stele.md: parse, stamp, hash, validate
internal/index/       index.json: load, save
internal/session/     state/<session>.json: arm state, touched dirs
internal/enforce/     the enforcement policy
internal/journal/     log.jsonl
internal/matcher/     request / corpus / fetch — pure logic
internal/backstop/    the hook decision — pure logic
internal/serve/       MCP wiring over the SDK
internal/install/     init, uninstall, markers, version stamp
assets/               embedded files init writes
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

The same shape applies to the parser: `corpus.Parse([]byte) ([]Rule, error)` is a
pure transformation, and stamping is `corpus.Stamp([]Rule) ([]Rule, []byte)` —
returning the new file bytes rather than writing them.

This is the single most important structural choice in the document. Everything
below is consistent with it.

---

## 3. Packages

### `internal/corpus` — the source of truth

Owns `stele.md` and nothing else.

```go
type Rule struct {
    ID          string   // UUID, stamped. empty before first compile
    Description string   // optional
    Tags        []string
    Scope       []string // path globs
    Body        string
    Line        int      // for error messages
}

func Parse(src []byte) ([]Rule, error)
func Stamp(rules []Rule, src []byte) (stamped []byte, added int, err error)
func Hash(body string) string
func Validate(rules []Rule) error
```

**`Parse`** splits on `---` at the start of a line, reads leading `key: value`
lines as metadata, and takes everything after the first blank line as the body.

**`Stamp`** is the delicate one, because it writes back into a file the user owns
(D8, as narrowed). Requirements:

- Idempotent. Stamping twice changes nothing.
- Byte-preserving. Only the inserted `id:` lines differ; every other byte,
  including whitespace and line endings, survives.
- Never reorders, never reformats, never touches a body.

It works on the original bytes with recorded offsets rather than re-rendering
parsed rules, because a re-render cannot preserve what it did not capture.

**`Hash`** normalises before hashing: lowercase, runs of whitespace collapsed,
leading and trailing whitespace and punctuation stripped per line, lines joined
with a single space. It is never identity — only staleness, incremental compile,
and drift (D20).

**`Validate`** returns `ErrDuplicateID` when two rules share an id, which happens
when someone copy-pastes a rule block.

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
func Save(dir string, idx Index) error // atomic
```

Gitignored and regenerated freely (D24), so the format carries no compatibility
obligation. `Version` exists so a mismatch triggers a rebuild rather than a parse
error.

### `internal/session` — shared state between two processes

```go
type State struct {
    SessionID string
    PromptID  string   // the current turn
    Armed     bool
    Touched   []string // directories written to this turn
    UpdatedAt time.Time
}
```

One file per session, `state/<session-id>.json`, per D37. Written by both the hook
process and the MCP process, so every write is atomic (§7).

### `internal/enforce` — policy

```go
type Policy struct {
    Backstop     bool // default true
    BlockOnError bool // default false
}
```

An option struct rather than functional options: both fields are always present,
both are read from a config file, and neither can fail. The Google style guide's
test for choosing between them points here.

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

### `internal/matcher` and `internal/backstop` — the logic

Pure, as in §2. `matcher` holds the three tool handlers' behaviour; `serve` holds
the MCP plumbing that calls them. Splitting them is the style guide's
"separate CLI code from library code" applied to a transport: the protocol is
another client of the logic, not the home of it.

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

**All writes are write-temp-then-rename.** `os.Rename` replaces atomically on
POSIX and on Windows via `MoveFileEx`, and the temp file is created in the same
directory so the rename never crosses a filesystem.

This matters most for `stele.md`. A crash mid-stamp must never leave a user's rule
file truncated.

**`journal.Append` opens with `O_APPEND`** and writes one line per call. Short
appends are atomic on POSIX. Windows offers no equivalent guarantee, so the writer
takes a lock file for the duration of the append.

**Session state needs no locking** across sessions, because each session owns its
own file. Within a session the hook and the MCP process can write concurrently, so
both use the same atomic replace.

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
assets/matcher-agent.md    → .claude/agents/stele-matcher.md
assets/block-message.txt   → the permissionDecisionReason text
assets/stele-header.md     → the header comment in a new stele.md
assets/gitignore           → .stele/.gitignore, containing "*"
```

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
| `corpus.Stamp` | Golden files. Round-trip byte equality. Idempotence asserted explicitly. |
| `backstop.Decide` | Table-driven over the full cross-product of state, policy and error. Pure, so no fixtures. |
| `matcher` | Table-driven. Cap, truncation, empty selection. |
| `index`, `session`, `journal` | Temp directories. Concurrent-write tests for the atomic replace. |
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
stele compile      parse, stamp, index; prints the drift report
stele check        parse and validate without writing (for CI)
stele mcp          run the MCP server on stdio
stele hook         read a PreToolUse payload on stdin, decide
```

Standard-library `flag` and a switch on `os.Args[1]`. Six subcommands with almost
no flags does not justify a CLI framework, and the dependency list stays at the
MCP SDK plus `go-cmp` for tests.

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

- **`stele check` in CI** — worth having, not yet specified. What does it exit
  non-zero on? Duplicate ids certainly. Unstamped rules, arguably.
- **Windows line endings in `stele.md`** — `Stamp` must preserve CRLF exactly.
  Covered by a golden test, but worth naming as a first-class requirement given
  the development environment.
- **`journal` rotation** — the log grows without bound. Not a v1 problem at one
  entry per turn, but it has no answer.
