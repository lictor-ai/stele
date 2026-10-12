# stele — Architecture and Technical Design

**Date:** 2026-08-29
**Status:** draft for review
**Implements:** `docs/superpowers/specs/2026-08-29-stele-v1-design.md`,
`docs/adr/0002-engine-and-extension-points.md` and
`docs/adr/0003-ready-for-a-dashboard.md`

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
internal/source/      the Source slot: Rule, the Source interface
  stelemd/            the built-in stele.md source: read, hash, parse
internal/lock/        stele.lock: identity, reconciliation
internal/safefile/    crash-safe replace, create-if-absent, lock files, safe dirs and bounded reads (§7)
internal/sign/        the per-machine key that signs generated files (D57)
internal/index/       index.json: load, save
internal/compile/     the one place that turns sources into an index
internal/session/     state/<session>.json: arm state, touched dirs
internal/enforce/     the enforcement policy
internal/journal/     log.jsonl
internal/matcher/     request / corpus / fetch — pure logic
internal/backstop/    the hook decision — pure logic
internal/serve/       MCP wiring over the SDK
internal/plugin/      registry of built-in and external plugins, exec protocol, sinks
internal/status/      stele status: describe the install as JSON
internal/check/       the Checker slot: stele check's runner and report
  regex/              the built-in regex checker
internal/adapter/     the agent adapter slot: the Adapter interface
  claudecode/         the built-in Claude Code adapter
internal/install/     init, uninstall, markers, version stamp
  assets/             embedded templates — must live here, see §9
```

`internal/` throughout. stele exports no library, so nothing lives at the module
root — the official guidance's server-project layout. Nothing is importable, which
means every package boundary can move later without breaking anyone.

**The public extension surface is the plugin protocol, not a Go API** (ADR-0002).
An external source or checker is a separate executable that speaks JSON over
stdin/stdout (`internal/plugin`). So stele can stay unimportable and still be
extended, in any language, by code it never links.

Each slot follows the same shape: the interface lives in the package that
consumes it (`source`, `check`, `adapter`), the built-in implementation is a
subpackage, and `internal/plugin` is the only package that knows about both
built-ins and executables.

No `util`, `common`, `helpers`, or `pkg`. Package names are chosen so the name and
the exported type read together: `source.Rule`, `session.State`, `enforce.Policy`,
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

The same shape applies to `stelemd.Parse([]byte) ([]source.Rule, error)`, to
`lock.Reconcile`, which is the identity core, and to `regex.Match`, which is the
whole built-in checker.

**Two things have to be injected for this to hold.** A `call_id` needs randomness
and a journal entry needs a timestamp, so "pure" here means *pure given a clock
and an id source passed in*, not *free of them*. `Reconcile` takes `newID func()
string`; anything needing the time takes a `func() time.Time`. Real implementations
in `cmd/`, fixed stubs in tests.

This is the single most important structural choice in the document. Everything
below is consistent with it.

---

## 3. Packages

### `internal/source` — the Source slot

**Only a source opens rule files, and only for two operations** (D40):

1. **Read and transform** — turn its files into rules, on the way to the index.
2. **Has it changed** — return a fingerprint to compare against the index.

Everything downstream takes `index.Index`. No other package is given a rule
file's path, so no other package can reach one.

**Read-only. There is no write path at all** (D38), and deliberately no method
that produces bytes.

```go
type Rule struct {
    Description string            // optional
    Tags        []string
    Scope       []string          // path globs
    Body        string
    Meta        map[string]string // every other key, kept as written (D46)
    ID          string            // optional; set only by a source that owns its ids (D52)
    Source      string            // the source's name, e.g. "stelemd"
    File        string            // relative to the root, for error messages
    Position    int               // ordinal within this source, for reconciliation
    Line        int               // for error messages
}

type Source interface {
    Name() string
    Read(ctx context.Context, root string) (rules []Rule, fingerprint string, err error)
    Fingerprint(ctx context.Context, root string) (string, error) // must be cheap
}

func Hash(body string) string // pure, shared by every source
```

`Rule.ID` is empty for every built-in source. Identity is not a property of a
rule file — it is assigned by `internal/lock`. The one exception is a source that
owns its ids, such as an org-wide source whose rules must share an id across
repositories (D52); the lock then records that id instead of minting one.

**`Fingerprint`** is the whole staleness mechanism. Compile compares it against
the fingerprint the index recorded for that source. That is why `index.Load` does
not report staleness: the index stores fingerprints but never interprets them,
and it has no business knowing where any rule file lives.

**`Hash`** normalises before hashing: lowercase, runs of whitespace collapsed,
leading and trailing whitespace and punctuation stripped per line, lines joined
with a single space. Used for reconciliation, staleness and drift — never as
identity (D20). It lives here, not in each source, so every source hashes alike
and a rule moving between sources keeps its id.

The name follows the design doc's vocabulary: *source rules* are what the user
writes, and the *corpus* is the compiled whole the matcher sees. This package
handles the first.

#### `internal/source/stelemd` — the built-in source

```go
func New() source.Source
func Parse(src []byte) ([]source.Rule, error) // pure
```

**`Parse`** splits on lines holding only `---`, except inside a fenced code
block. It skips leading HTML comments, reads leading `key: value` lines as
metadata, and takes everything after the first blank line as the body. A key
matches `^[a-z][a-z0-9_-]*$` and is followed by `:` and a space or the end of the
line; any other line is body. `description`, `tags` and `scope` fill their
fields. **Every other key goes into `Meta` unchanged**, so the format can grow
without a parser change. `check` and `blocking` are among them: they matter to
`internal/check`, not to the parser. The full format is spec §3.

**Bad input is a `*source.ParseError`**, which carries the file and line and
prints as `stele.md:12: rule has no body`. Every source uses it, so every parse
error reads alike.

**`Read`** returns the same fingerprint `Fingerprint` would, computed from the
bytes it parsed, so a file edited between the two calls cannot be recorded with
the wrong fingerprint.

**`Fingerprint`** hashes the raw bytes of `stele.md`. An absent file is zero rules
and an empty fingerprint, not an error — a repository may take all its rules from
other sources.

Because nothing here writes, the byte-preservation problem disappears entirely,
and with it the CRLF concern that was open in §13.

### `internal/lock` — identity

Owns `stele.lock`: committed, stele-written, never hand-edited.

```go
type Entry struct {
    ID       string `json:"id"`
    Source   string `json:"source"`   // the source the rule came from
    Hash     string `json:"hash"`
    Position int    `json:"position"` // within that source
}

type File struct {
    Version int     `json:"version"`
    Project string  `json:"project"` // this repository's UUID, minted once (D51)
    Rules   []Entry `json:"rules"`
}

type Report struct {
    Supplied, ByHash, ByPosition, Minted, Dropped int
    Guessed []string // ids matched by position — reported, because they are inferences
    Rekeyed []Rekey  // lock ids replaced because the source started supplying one
}

type Rekey struct{ Old, New string }

func Load(root string) (File, error)
func Save(root string, f File) error
func Validate(f File) error // ErrDuplicateID, ErrInvalid
func Equal(a, b File) bool  // would save identically: a lock on disk not Equal to the planned one is out of date

// Reconcile is pure. newID is injected so it stays testable.
func Reconcile(rules []source.Rule, prev File, newID func() string) (ids []string, next File, rep Report, err error)
```

`Reconcile` is the identity core, and it runs in three passes after one step
that is not a guess:

0. **Supplied id** → the rule keeps the id its source gave it (D52). A supplied
   id that clashes with another rule's is `ErrDuplicateID`, returned as `err`.
   A rule whose id changes because its source started supplying one is
   reported in `Report.Rekeyed`.
1. **Exact hash match** → same rule. Survives reordering, and a rule moving to
   another source.
2. **Remaining rules, matched by source and position** → same rule. Survives
   rewording, which is the case that matters, since §7 exists to prompt
   rewording. Positions are only compared within one source: position 3 of
   `stele.md` says nothing about position 3 of `.cursor/rules`.
3. **Still unmatched** → new rule, mint an id.

Pass 2 is an inference, so every id it assigns lands in `Report.Guessed` and gets
printed. A wrong guess moves a rule's log history to the wrong rule, and that must
be visible in a diff rather than silent.

`newID` is a parameter rather than a call to `uuid.NewString` so `Reconcile` stays
a pure function — see §2's note on injected sources. It also mints `Project`
when `prev` has none; once set, `Reconcile` never changes it.

**`Validate`** returns `ErrDuplicateID` when two entries share an id. That cannot
happen from `Reconcile`, which mints fresh UUIDs, but it can happen from a merge
that duplicated an entry. It runs on every load, because two rules silently
sharing a history is worse than a loud failure. It also rejects an entry with no
id and a lock with no `project` as `ErrInvalid`, as `Load` does for bad JSON or
another version: only a hand edit produces them, and accepting them would let one
rule claim two entries or mint a second project id.

### `internal/index` — the build artifact

```go
type Index struct {
    Version int
    Project string            // copied from stele.lock, so the hot path never opens the lock
    Sources map[string]string // source name → fingerprint at compile
    Rules   []Entry
}

type Entry struct {
    ID, Source, Description, Body string
    Position                      int // within its source, so EnsureFresh can carry the rule over
    Tags, Scope                   []string
    Meta                          map[string]string
    BodyHash                      string
}

func Load(root string, key sign.Key) (Index, error) // ErrNoIndex, also when not signed with key. never ErrStale
func Save(root string, idx Index, key sign.Key) error // signed, crash-safe replace, §7
func Equal(a, b Index) bool
```

Gitignored and regenerated freely (D24), so the format carries no compatibility
obligation. `Version` exists so a mismatch triggers a rebuild rather than a parse
error: `Load` returns `ErrNoIndex` for a missing, unparsable or other-version
index alike, and for one that is not a plain file or is over 64 MiB (§7). `Save` creates `.stele/` and its `.gitignore` (`*`) when they are
missing, so a compile before `init` never leaves the index committable. The
`.gitignore` is created whole or not at all. `.stele/` must be a real directory:
the repository's own `.gitignore` does not cover it, so a cloned repository could
commit it as a symlink, and stele refuses to write through one.

### `internal/compile` — the orchestration

Compiling is these steps across four packages:

```
each Source.Read → lock.Load → lock.Reconcile
                 → lock.Save → build the index → index.Save
```

Three callers need that sequence: `stele compile`, `stele check`, and `request`
when the index is stale. Without a package owning it, it gets duplicated between
`cmd/` and `serve/`.

```go
type Result struct {
    Index  index.Index
    Lock   lock.File
    Report lock.Report
    LockChanged  bool // the planned lock differs from stele.lock: for check, out of date
    IndexChanged bool // the planned index differs from the one on disk, or there is none
}

// Plan reads every source and writes nothing.
// It holds no key, so IndexChanged is always true: stele check never serves the index.
func Plan(ctx context.Context, root string, srcs []source.Source, newID func() string) (Result, error)
// Compile is stele compile: read every source, then Plan and Apply under the compile lock.
func Compile(ctx context.Context, root string, srcs []source.Source, newID func() string, key sign.Key) (Result, error)
// Apply writes the lock and the index, each only when it changed, lock first.
func Apply(root string, r Result, key sign.Key) error
// EnsureFresh re-reads only builtins, and only those whose fingerprint changed.
// Rules from every other source are carried over from the current index.
func EnsureFresh(ctx context.Context, root string, builtins []source.Source, newID func() string, key sign.Key) (index.Index, bool, error)
```

The `Plan`/`Apply` split is what makes `stele check` possible: CI runs `Plan`,
inspects the `Report`, and never writes. `EnsureFresh` is the one `request` calls
— it asks each built-in source for its `Fingerprint`, and only when one differs
does it re-read that source, reconcile and `Apply`. The returned bool says
whether it recompiled, so the caller can log it.

**`EnsureFresh` takes built-in sources only** (D45). An external source may be a
network call to Confluence; it runs at `stele compile` and `stele check`, never
while the agent waits. Its rules stay in the index as they were last compiled. If there is no usable
index — none yet, or a `Version` mismatch — `EnsureFresh` builds one from the
built-ins alone, and external rules come back at the next `stele compile`. That
rebuild keeps the lock entries of the sources it did not read, so the external
rules come back with their ids. The same holds when the index exists but no
longer lists an external source: `EnsureFresh` never reads one, so it never drops
one's lock entries.

**Compiles are serialised** by `.stele/compile.lock`, held across "load the lock →
reconcile → write the lock → write the index" in `Compile` and `EnsureFresh`.
Without it two `stele mcp` processes, or one and `stele compile`, can each mint an
id for the same new rule and leave the lock and the index disagreeing, with
fingerprints that match so nothing repairs it. Source reads stay outside the lock,
so a slow external source never holds it. `Plan` takes no lock: it writes nothing,
and every file it reads is replaced whole.

`compile` never resolves plugins itself. `cmd/` asks `internal/plugin` for the
sources and passes them in, which keeps `compile` free of `PATH`, executables and
`stele.json`.

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

func Load(root, id string, key sign.Key) (State, error) // no file yet: a zero State carrying id
func Update(root, id string, now time.Time, key sign.Key, fn func(*State) error) (State, error)
func SetCurrent(root, id string) error     // the hook, on every block
func Current(root string) (string, error)  // request; "" when no session has blocked
```

One file per session, `state/<session-id>.json`, per D37. The id comes from the
host agent, or from `state/current`, which a cloned repository can commit, and
becomes part of a path. So anything but letters, digits, `_` and `-` is
`ErrBadSessionID`, and so is a Windows device name (`CON`, `NUL`, `COM1`, …),
since `CON.json` is the console whatever the extension.

`Load` and `Current` read through `safefile.ReadRegular`: a regular file only,
never a symlink or a device, capped in size. A `current` that is not a plain file
names no session. A state file that cannot be read — not JSON, not a regular file,
too large — fails `Load`, and `Update` replaces it with a fresh state rather than
fail every update of that session.

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

`Update` is that read-modify-write: it takes `state/<id>.json.lock` with
`safefile.Lock`, loads, applies `fn`, saves through the crash-safe replace, and
releases the lock. It waits at most 2 seconds for another holder, since the hook
runs on every write, and takes over a lock older than 10 seconds as left by a
crashed process.

`safefile.Lock` creates the lock file exclusively, which works alike everywhere,
and writes a token unique to its holder into it. Unlocking removes the file only
while it still holds that token, so a holder that was taken over never removes its
successor's lock. A stale lock is taken over by renaming it away, which only one
of several racing processes can do. A lock that is not a plain file — a directory
or a symlink a cloned repository committed — is never one stele made, and is moved
aside at once. On Windows, creating a lock file that was just removed while
another process still has it open fails with access denied, so that also counts
as held.

After saving, `Update` removes session files not updated for 24 hours, each under
that session's lock and only if it is still old once the lock is held. Lock, temp
and `.stale` files older than 24 hours go too, checked again just before removal.
`.stele/state` must be a real directory, as `.stele/` must: a committed symlink
would otherwise make stele write, and delete old `*.json` files, wherever it
points. A directory or link where a file belongs is moved aside by the write (§7).

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

The same file also lists plugins. `enforce.Load` decodes the two policy keys and
ignores the rest; `internal/plugin` decodes `sources` and `checkers` and ignores
the rest. The hook only ever needs the policy, so it never pays for plugin
resolution.

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
    V          int       `json:"v"`       // record format version, 1 (D50)
    Project    string    `json:"project"` // from stele.lock (D51)
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

Pure, as in §2.

```go
type Input struct {
    PromptID  string
    AgentType string  // non-empty inside a subagent
    Path      string  // the file about to be written
    HasRules  bool    // the index exists and is non-empty
}

func Decide(in Input, st session.State, p enforce.Policy) Decision
```

`Input` is stele's assembled view, not the raw hook payload. `cmd/` builds it from
what Claude Code sent plus one cheap `index.Load`. `HasRules` is deliberately a
single bit — **existence, not freshness.** The hook runs on every write and never
reads rule content, so it has no business parsing or recompiling anything.

One function, and its order is the whole behaviour:

```
1. AgentType is "stele-matcher"      → allow      never block our own subagent
2. !policy.Backstop                  → allow      the backstop is off
3. !HasRules                         → allow      nothing to deliver
4. PromptID differs from state       → ARM, reset Touched, Blocks, LastError
5. disarmed, dir not in Touched      → RE-ARM
6. disarmed                          → allow, record the directory
   ── armed from here ──
7. LastError is set                  → failure path
8. Blocks >= 3                       → failure path
9. otherwise                         → BLOCK, Blocks++

failure path:  policy.BlockOnError ? block, naming the error : allow, warn
```

**Step 3 is the first thing a new user hits.** Right after `init` there are no
rules yet. Blocking a write to demand rules that do not exist would make stele
look broken on the very first turn.

Steps 7 and 8 are the two halves of the same problem. **7 handles failures that
produced an error** — `stele mcp` recorded it, so the hook can act on it.
**8 handles failures that produced nothing** — the exchange simply stopped, which
is indistinguishable from an agent ignoring the redirect. The counter does not
need to know which; it only needs to know the exchange is not completing.

Three is a placeholder. One would fire on the normal path, where the agent is
blocked once and complies. Two would fire on a legitimate retry. Three gives two
real attempts, and §13 of the spec lists it as a number the logs should settle.

### `internal/matcher` and `internal/serve`

`matcher` holds the three tool handlers' behaviour; `serve` holds the MCP plumbing
that calls them. Splitting them is the style guide's "separate CLI code from
library code" applied to a transport: the protocol is another client of the logic,
not the home of it.

`matcher` is also what writes `LastError` — every failure path in `request` or
`corpus` records itself before returning, because a failure the hook cannot see is
a failure the policy cannot act on.

`corpus` and `fetch` never carry `Meta`. Checker settings are for CI; the matcher
and the agent need the prose (spec §3).

### `internal/plugin` — the registry and the exec protocol

The only package that knows a plugin can be either compiled in or an executable.
Everything else sees `source.Source` and `check.Checker`.

```go
type Config struct {
    Sources  []Ref `json:"sources"`
    Checkers []Ref `json:"checkers"`
    Sinks    []Ref `json:"sinks"`
}

type Ref struct {
    Name        string          `json:"name"`
    Config      json.RawMessage `json:"config,omitempty"` // passed to the plugin untouched
    ShareBriefs bool            `json:"share_briefs,omitempty"` // sinks only (D53)
}

func LoadConfig(root string) (Config, error) // reads stele.json; absent means empty

func Sources(root string, c Config) ([]source.Source, error)  // built-ins first, then externals
func Builtins() []source.Source                               // what EnsureFresh gets
func Checkers(root string, c Config) (map[string]check.Checker, error)
func Sinks(root string, c Config) ([]Sink, error)             // externals only
```

**Built-ins** are registered by name: `stelemd` for sources, `regex` for
checkers. They are always present and need no entry in `stele.json`. There is no
built-in sink: every real destination is a network call, and the engine stays
offline (ADR-0003).

**Externals are resolved by one function**, and it is the security boundary
(D43):

1. The name must match `^[a-z][a-z0-9-]*$`. No separators, no dots, so no path.
2. It must not be a built-in name.
3. `exec.LookPath("stele-source-" + name)` (or `stele-checker-`). Go already
   refuses a result found through a relative `PATH` entry such as `.`
   (`exec.ErrDot`); stele does not override that.
4. The resolved path, after `filepath.EvalSymlinks`, must not be inside the
   project root. A `PATH` that names `./bin` by absolute path is caught here.

A failure at any step is `ErrPluginNotFound` or `ErrPluginRefused`, and the
message names the executable it looked for. Nothing is ever run from a path
written in `stele.json`, because `stele.json` is committed and a clone must not
run code it brought with it.

**The protocol, version 1.** One process per call: stele writes one JSON request
to stdin and closes it, the plugin writes one JSON response to stdout and exits 0.

| `op` | Request | Response |
| :-- | :-- | :-- |
| `describe` | — | `{"kind": "source" \| "checker" \| "sink", "protocols": [1], "version": "1.4.0"}` |
| `fingerprint` | `root`, `config` | `{"fingerprint": "…"}` |
| `read` | `root`, `config` | `{"fingerprint": "…", "rules": [{file, position, line, description, tags, scope, body, meta}]}` |
| `validate` | `config`, `rules: [{id, meta}]` | `{"problems": [{"rule_id", "message"}]}` |
| `check` | `root`, `config`, `files`, `rules: [{id, body, scope, meta}]` | `{"violations": [{rule_id, file, line, column, message}]}` |
| `push` | `config`, `project`, `records: [...]` — log entries and check reports, each with its own `v` | `{"accepted": n}` |

A `read` response may set `id` on a rule; only a source that owns its ids should
(D52).

**Sinks** (ADR-0003). `stele push` reads `.stele/log.jsonl` from the offset
stored in `.stele/push/<sink>.json`, sends the new entries, and advances the
offset only after the sink accepts them. `stele check --push` sends the report
it just produced. Unless `share_briefs` is set for that sink, `brief` is removed
from every record before it is sent. Neither runs from `stele hook` or
`stele mcp`.

Every request and response carries `"protocol": 1`. A plugin sent a version it
does not speak answers `{"protocol": 1, "error": "unsupported protocol 2; speaks [1]"}`.
Any response may be `{"error": "…"}` instead of a result. stderr is passed
through to the user. A non-zero exit, invalid JSON or a missing field is
`ErrProtocol`, naming the plugin.

Each call has a timeout: 30 seconds for `read` and `check`, 5 for the rest,
overridable per plugin in `stele.json` (`"timeout": "2m"`). `describe` runs once
per process, before the first real call, so a plugin of the wrong kind fails
early and clearly.

The adapter between the protocol and the interfaces — `execSource` and
`execChecker` — lives here and is unexported. To `compile` and `check`, an
external source is indistinguishable from `stelemd`.

### `internal/check` — `stele check`

```go
type Checker interface {
    Name() string
    Validate(rules []Rule) []Problem                         // settings, before any file is read
    Check(ctx context.Context, root string, rules []Rule, files []string) ([]Violation, error)
}

type Rule struct {          // what a checker sees of index.Entry
    ID, Body string
    Scope    []string
    Meta     map[string]string
}

type Problem struct {
    RuleID, Message string
}

type Violation struct {
    RuleID       string
    File         string
    Line, Column int
    Message      string
}

type Report struct {
    V          int          `json:"v"`       // 1 (D50)
    Project    string       `json:"project"` // D51
    Problems   []Problem    // invalid rules: exit 1
    Violations []Finding    // Violation + Blocking + rule excerpt
}

func Run(ctx context.Context, root string, idx index.Index, files []string, cs map[string]Checker) (Report, error)
func (r Report) ExitCode() int // 0, 1; errors from Run are 2
```

`Run` groups rules by their `check` key and calls each checker once, with only
its own rules and, per rule, only the files that match the rule's `scope`. A rule
whose `check` names no known checker is a `Problem`, not a skip. Whether a
violation is blocking is read from the rule's `blocking` key — default `true`
when `check` is set — never from the checker (spec §8).

`Run` takes an `index.Index`, not rule files: `cmd/` calls `compile.Plan` first
and hands over the planned index (D40). `Plan`'s `Report` also tells `cmd/`
whether the lock is out of date, which is a `Problem` too.

**Choosing files is `cmd/`'s job**, through `git`: `git diff --name-only
--diff-filter=ACMR` against `HEAD` or `<base>...HEAD`, `git ls-files` for
`--all`, or the paths given. `check` receives a plain list of paths relative to
the root, so it never runs `git` and its tests need no repository.

#### `internal/check/regex` — the built-in checker

```go
func Match(pattern *regexp.Regexp, content []byte) []Location // pure
```

Reads `pattern` from `Meta` and compiles it with Go's `regexp` (RE2). `Validate`
reports a missing or invalid pattern. `Check` reads each file once, runs every
rule's pattern over the whole content, and turns each match offset into a line
and column. An optional `message` key replaces the default message. Binary
files — a NUL in the first 8 KB — are skipped.

### `internal/status` — `stele status`

```go
type Status struct {
    V        int           `json:"v"` // 1 (D50)
    Project  string        `json:"project"`
    Binary   string        `json:"binary_version"`
    Stamped  string        `json:"installed_version"` // what init stamped
    Adapters []AdapterInfo `json:"adapters"`          // which artifacts are present
    Plugins  []PluginInfo  `json:"plugins"`           // kind, name, found on PATH, version from describe
    Sources  []SourceInfo  `json:"sources"`           // name, fingerprint, rule count
    Fresh    bool          `json:"index_fresh"`
}

func Collect(ctx context.Context, root string, c plugin.Config, adapters []adapter.Adapter) (Status, error)
```

Read-only. It runs `describe` on external plugins to learn their versions, so
like `compile` it may start plugins, and like `compile` it never runs on the hot
path. Without `--format json` it prints the same as a short human summary.

This is the whole surface a dashboard uses to see an install (D54). A dashboard
never writes to a repository; it proposes a change to `stele.json` that a human
reviews.

### `internal/adapter` — the agent adapter slot

Everything that knows one agent's shape. Adapters are always compiled in
(D44): they run on the hot path, so there is nothing to resolve.

```go
type Adapter interface {
    Name() string
    Artifacts(version string) []Artifact                    // what init writes for this agent
    ParseHook(stdin []byte) (backstop.Input, string, error) // the input and the session id
    RenderDecision(d backstop.Decision) (stdout []byte, exitCode int)
}

type Artifact struct {
    Path   string // relative to the root
    Merge  bool   // a JSON entry merged into an existing file, not a whole file
    Bytes  []byte
}
```

`Artifact` is defined here, not in `install`, so `install` imports `adapter` and
never the reverse.

`claudecode` is the only implementation in v1. It owns everything in §4: the
payload fields, the `hookSpecificOutput` shape, the `.claude/settings.json` hook
entry, the MCP registration and the matcher subagent file. `backstop.Decide` and
`install` stay agent-neutral; a Cursor adapter adds a subpackage and touches
neither.

### `internal/install`

Writes five marked artifacts and removes exactly those (D28). Three of them come
from the adapter's `Artifacts`. Every artifact carries `stele:v<version>` in a
comment or a JSON field so `uninstall` can identify its own work and leave hand
edits alone.

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

Walk up from `cwd` for a directory containing `stele.md` or `stele.json`. If none,
walk up for one containing `.git/` and use that. `stele.json` counts because a
repository may take all its rules from other sources and have no `stele.md`.

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
antivirus, or the search indexer will do — or stele itself, reading the file in
another process. So the rename retries those errors, access denied and sharing
violation, and only those, for up to 2 seconds; anything else fails at once. On final failure it
reports the likely cause rather than an opaque `*LinkError`. Reads retry the same
way: on Windows a read that meets another process's rename fails with a sharing
violation or access denied, which `safefile.ReadRegular` retries rather than
reporting.

**stele trusts only what it would have written itself.** Nothing under `.stele/`
is covered by the repository's own `.gitignore`, so a repository can commit
anything there, and `stele.lock` is committed by design. Every file stele uses has
an expected shape, and anything else gets one of two outcomes:

| Path | Expected | Otherwise |
| :-- | :-- | :-- |
| `.stele/`, `.stele/state/` | real directories | refused: "delete it, stele recreates it" |
| `stele.lock` | plain file, ≤ 16 MiB | refused, `ErrInvalid`: restore it, or delete it and compile |
| `.stele/index.json` | plain file, ≤ 64 MiB | `ErrNoIndex`: rebuilt, and the write moves it aside |
| `state/<id>.json`, `state/current` | plain file, small | a fresh state / no session; the write moves it aside |
| `*.lock` | plain file holding a token | a non-file is moved aside at once; a plain file once stale (§3) |

Every read of a file under `.stele/` or of `stele.lock` goes through
`safefile.ReadRegular` — `stele.md` is not covered: a user may symlink it on
purpose. It reads a regular file only, never through
a symlink, never a device, capped in size, so a link to `/dev/zero` cannot hang
stele or exhaust its memory. `safefile.Replace` moves anything but a plain file
out of its way before the rename, which callers rely on only for files stele can
regenerate or has just read as a plain file. A non-empty directory moved aside
stays, renamed `*.stale`; nothing reads it again. Session ids that are Windows
device names (`CON`, `NUL`, …) are rejected, since `CON.json` is the console
whatever the extension.

The shape check stops files that would crash or hang stele. It says nothing about
who wrote a well-formed file: a committed `index.json` with a matching fingerprint
would still be trusted. **So generated files are signed** (D57, `internal/sign`):

- On first use stele creates a random 32-byte key in the user's config directory
  (`os.UserConfigDir()/stele/key`, 0600), outside every repository. Two processes
  creating it at once agree on one. A key that cannot be read is an error, never a
  reason to trust unsigned files.
- `index.json` and `state/<id>.json` start with a line `stele-hmac-sha256 <hex>`,
  an HMAC-SHA256 over a purpose (`index`, `session:<id>`) and the body. The
  purpose stops a signed file passing for another kind, or another session's.
- A file that does not verify was not written by stele on this machine: an index
  is `ErrNoIndex` and rebuilt from the sources, so its rules are never served; a
  session state starts afresh.
- `stele check` loads no key: `Plan` never serves the index, so CI creates none.
- `stele.lock` is shared on purpose and is not signed. It holds no rule text, and
  changes to it show in review. `state/current` is not signed either: it only
  names a session, whose state must verify.

Malware already running as the user can read the key; nothing file-based stops
that, as for git or any local tool.

**`journal.Append` opens with `O_APPEND`** and writes one line per call. Short
appends are atomic on POSIX. Windows offers no equivalent guarantee, so the writer
takes a lock file for the duration of the append.

**Session state is locked per session**, not across sessions, because each session
owns its own file. Within a session the hook and the MCP process both
read-modify-write one record, so `session.Update` holds that session's lock file
for the duration (§3). A state file that cannot be read is replaced by a fresh
state, so one corrupt file never blocks a session.

**Stale state is cleaned on write:** any `state/*.json` older than 24 hours is
removed, under that session's lock and only if it is still old once the lock is
held, so an update landing meanwhile is never deleted. Lock, temp and `.stale`
files older than 24 hours, which only a crash leaves, go too. Nothing tells stele
that a session ended.

---

## 8. Errors

Following Code Review Comments and the style guide.

**Sentinel errors for conditions callers branch on**, inspected with `errors.Is`:

```go
var (
    ErrNoIndex        = errors.New("no index")             // index
    ErrDuplicateID    = errors.New("duplicate rule id")    // lock
    ErrInvalid        = errors.New("invalid")              // lock: not a plain file, too large, bad JSON, version, project or id
    ErrPluginNotFound = errors.New("plugin not found")     // plugin
    ErrPluginRefused  = errors.New("plugin refused")       // plugin: inside the repo, bad name
    ErrProtocol       = errors.New("plugin protocol error") // plugin
)
```

There is no `ErrStale`. Staleness is not an error and not the index's business —
it is a comparison of a source's `Fingerprint` with the index, and only a source
can compute a fingerprint (D40). There is no `ErrNoRulesFile` either: a missing
`stele.md` is zero rules, because other sources may supply them.

`stele check` maps the plugin errors to exit 2, never 0: a check that could not
run has not passed.

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
binary. `matcher-agent.md` is written to disk by design — spec §12 records that as
a known weakness, since it lands in a file the governed agent can edit.

The block message and the matcher prompt are the two highest-leverage strings in
the project. Design doc §4.2 is explicit that a redirect reading as *call this
instant* gets obeyed literally, thin brief and all. They are versioned assets, not
string literals scattered through the code, so they can be revised and diffed.

---

## 10. Testing

| Layer | Approach |
| :-- | :-- |
| `stelemd.Parse` | Table-driven. Malformed input, no metadata, empty bodies, CRLF, unicode, unknown keys kept in `Meta`, a body opening with `Note:`, `---` inside a code block, header comments. Pure, so no fixtures. |
| `stelemd` fingerprint | Temp directory. Unchanged, edited, missing file. |
| `lock.Reconcile` | Table-driven with a stub `newID`. Reorder, reword, insert, delete, reword-plus-reorder — the case that mismatches — and a rule moving between two sources. |
| `compile.Plan` / `Apply` | Temp directory with a real `stele.md` and a fake second source. Round-trip, then edit and recompile to check ids carry over. `Plan` asserted to write nothing. `EnsureFresh` asserted never to call a non-built-in source. |
| `plugin` resolution | Temp `PATH`. Found, not found, bad name, built-in name reused, executable inside the root, symlink into the root. |
| `plugin` protocol | A tiny Go test plugin built by `TestMain`, scripted by an env var: good answer, wrong protocol, `error` field, invalid JSON, non-zero exit, timeout. |
| `check.Run` | Table-driven with a fake checker. Grouping by `check`, `scope` filtering, `blocking` default, unknown checker, exit codes. |
| `lock.Reconcile`, ids from a source | Supplied id kept, clash is an error, `Project` minted once and never changed. |
| sinks / `stele push` | The test plugin as a sink: offset advances only on success, no record sent twice, `brief` stripped unless `share_briefs`. |
| `status` | Temp directory: JSON shape and `v`, plugin found and missing, stale index. |
| `regex` | Table-driven over `Match`: line and column, multiple matches, multi-line content, CRLF, binary skip, invalid pattern. |
| `backstop.Decide` | Table-driven over the full cross-product of state, policy, error and `HasRules`. Pure, so no fixtures. |
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
stele compile      every source, then compile.Plan + compile.Apply; prints the reconcile report
stele check        validate the rules, run checkers on changed files; writes nothing (for CI)
                     [--base <ref>] [--all] [--format text|json] [--push] [path...]
stele status       describe the install [--format text|json]
stele push         send new log entries to every listed sink
stele mcp          run the MCP server on stdio
stele hook         read a PreToolUse payload on stdin, decide
```

Standard-library `flag` and a switch on `os.Args[1]`. Eight subcommands with a
handful of flags does not justify a CLI framework.

`compile`, `check`, `status` and `push` are the only commands that run external
plugins. `mcp` and `hook` never do (D45).

`check` exits 0 when the rules are valid and nothing blocking was found, 1 for an
invalid rule or a blocking violation, and 2 when stele or a plugin could not run
(spec §8). It needs `git` on `PATH` unless explicit paths are given.

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

- **Protocol version 2.** Nothing needs it yet. When something does, stele has
  to speak both versions for a while, and `describe` is where it finds out which
  one a plugin speaks.
- **`journal` rotation** — the log grows without bound. Not a v1 problem at one
  entry per turn, but it has no answer.

*Windows line endings were open here while compile wrote back to `stele.md`. D38
removed the write path, so nothing has to preserve them.*

*`stele check` in CI was open here. Spec §8 now specifies it: invalid rules,
duplicate ids and a lock out of date with the sources all fail.*
