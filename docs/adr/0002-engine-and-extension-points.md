# Engine and extension points

* Status: accepted
* Date: 2026-10-11
* Supersedes: [ADR-0001](0001-guideline-storage-format.md)

## Context and Problem Statement

stele is for every developer first. It is open core: the free engine has to be
useful on its own, and paid add-ons come later.

The v1 spec (`docs/superpowers/specs/2026-08-29-stele-v1-design.md`) describes the
coding-time tool well: a `stele.md` file, a lockfile of UUIDs, an MCP server with a
matcher subagent, and a Claude Code hook that blocks the first write until rules
are fetched. That stays.

What the spec does not have is an engine with parts that can be swapped:

* It reads exactly one input format.
* It has no plugin interfaces.
* It never checks code, so CI has nothing to run.
* It says "nothing is importable", so there is no way to extend stele at all.

ADR-0001 tried to fill some of this gap. It added a heavier format (guidelines
with owner, version, severity and hand-written rule IDs) that is compiled into
each agent's native rule files. That conflicts with the spec on file location,
format, identity and delivery, and it is too heavy for a single developer. This
ADR supersedes it.

We need to decide:

1. Which parts of stele can be swapped.
2. How a plugin is loaded, and in what language it can be written.
3. What stays free and what can be paid.

## Considered Options

* **Option 1:** Three fixed slots (source, agent adapter, checker). Built-ins are
  compiled in. External plugins are executables that speak versioned JSON over
  stdin/stdout.
* **Option 2:** Go plugins (`plugin` package) or a public Go API under `pkg/`.
* **Option 3:** WebAssembly plugins run inside the binary.
* **Option 4:** No plugins. Every source and checker is built into the binary.

## Decision Outcome

Chosen option: **Option 1**. It lets a plugin be closed source and written in any
language, which is how paid plugins ship. It keeps the binary free of anything a
developer did not ask for. And it needs no public Go API, so `internal/` stays as
it is.

### The three slots

| Slot | What it does | v1 ships | Later |
| :-- | :-- | :-- | :-- |
| **Source** | Turns rule files into rules. | The `stele.md` source. | `.cursor/rules` and `.claude/rules`, read deterministically with no inference. Paid: rule extraction from guideline prose; Confluence, SharePoint, PDF. |
| **Agent adapter** | Installs and runs stele in one coding agent: the hook, the MCP registration, the subagent and any skills. | Claude Code. | Cursor next. |
| **Checker** | Checks code against rules and reports each violation with its rule ID. This is what CI runs. | A basic `regex` checker. | Semgrep, and checks judged by an LLM. |

Every source produces the same thing: rules with a body and metadata. They all go
through the same lockfile, so a rule from `.cursor/rules` gets a stable ID exactly
like a rule from `stele.md`. The lock records which source each rule came from.

A checker never decides whether a violation fails CI. It reports what it found,
and stele reads `blocking` from the rule.

### Agent adapters are always compiled in

The hook runs on every file write, and the MCP server answers every request.
That is the hot path, so an adapter cannot be a process stele has to find and
start first. Adapters are built into the binary and are always free. Adding an
agent means a new stele release, which is acceptable because there are few agents
and each one needs real work anyway.

### Loading plugins

**Built-in plugins are compiled in:** the `stele.md` source, the `regex` checker,
and the Claude Code adapter. They need no configuration.

**External plugins are executables**, named `stele-source-<name>` and
`stele-checker-<name>`. This works the way `git` finds `git-<name>` subcommands and
the way Terraform runs providers. A plugin can be closed source and written in any
language.

They speak versioned JSON over stdin and stdout:

* stele starts the process, writes **one request** to stdin and closes it.
* The plugin writes **one response** to stdout and exits.
* Both carry `"protocol": 1`. A plugin that does not speak the version it is sent
  answers with an error naming the versions it does speak.
* stderr is free-form and is passed through to the user.
* A non-zero exit or output that is not valid JSON is a plugin failure.

```json
→ {"protocol": 1, "op": "read", "root": "/repo", "config": {"space": "ENG"}}
← {"protocol": 1, "fingerprint": "…", "rules": [{"file": "…", "position": 0, "line": 12, "tags": ["db"], "scope": [], "body": "…", "meta": {}}]}

→ {"protocol": 1, "op": "check", "root": "/repo", "files": ["api/user.go"], "rules": [{"id": "…", "body": "…", "scope": ["**/*.go"], "meta": {"pattern": "…"}}]}
← {"protocol": 1, "violations": [{"rule_id": "…", "file": "api/user.go", "line": 41, "column": 7, "message": "…"}]}
```

The full message set is in `docs/architecture.md`, `internal/plugin`.

**External plugins never run on the hot path.** `stele compile` and `stele check`
run them. `request` only refreshes built-in sources, and keeps the rules an
external source produced at the last compile. A hook or a matcher that waits on a
Confluence API is a tool people uninstall.

### Which plugins may run

**Only plugins listed in `stele.json` and found on `PATH` ever run.**

```json
{
  "sources":  [{"name": "cursor-rules"}, {"name": "confluence", "config": {"space": "ENG"}}],
  "checkers": [{"name": "semgrep"}]
}
```

* A name is a plain identifier. It never contains a path separator.
* stele looks the executable up on `PATH` and nowhere else.
* If the executable it finds is inside the repository, stele refuses to run it.
  This covers a `PATH` that contains `.` or a project `bin/` directory.
* An external plugin cannot reuse a built-in name.

This matters because `stele.json` is committed. Cloning a repository and running
`stele check` must never run code the repository brought with it. A plugin runs
only if the developer installed it on their own machine.

### Free and paid

**If a single developer would want it, it is free.** That covers the engine, every
agent adapter, the basic sources (`stele.md`, `.cursor/rules`, `.claude/rules`),
the `regex` checker, and the plugin protocol itself.

**Paid means needs that only an organization has:** rule extraction, enterprise
parsers (Confluence, SharePoint, PDF), central governance across many
repositories, and audit.

**Rule extraction is paid and comes after v1.** It means a team writes
guidelines as normal prose, and a source plugin pulls out the individual rules,
each with a stable id in `stele.lock`. That is the design doc's derived-entry
path (`O-ENTRY`). It is a paid source plugin, not part of the free engine:

* The engine never calls a model (D7, D22), and extraction needs one.
* Teams with large written standards are organizations; a single developer
  writes a handful of rules in `stele.md` directly.

The free engine still reads only what the user writes as rules. Nothing in v1
infers rules from prose (D17).

The line is about who needs a feature, not about how hard it was to build. The
protocol is public and stable, so anyone can write a plugin, free or paid.

### What happens to ADR-0001

It is superseded. Its ideas (owners, versions, severity, guideline documents,
compiling rules into each agent's native files) are kept as post-v1 enterprise
ideas, not as the v1 format. `examples/guidelines/` used its format, which v1 does
not parse, so it is removed.

One idea carries over in a lighter form: the rule format keeps unknown metadata
keys instead of rejecting them, so `check:`, `pattern:`, `blocking:` and later an
`owner:` can be added without a new format.

### Positive Consequences

* A developer gets a working tool from one binary and one file, with nothing to
  configure.
* New sources and checkers ship without a stele release.
* Paid plugins can be closed source without forking or linking the engine.
* `internal/` stays as it is. The public contract is the JSON protocol, which is
  smaller and easier to keep stable than a Go API.
* Running code from a cloned repository is ruled out by design, not by a warning.

### Negative Consequences

* Each external plugin call starts a process. That is fine in CI and at compile
  time, and it is why external plugins stay off the hot path.
* The protocol is a public API now. Every change needs a version bump and a
  period where both versions work.
* A plugin listed in `stele.json` but not installed on a teammate's machine fails
  `stele check` for them. The error has to name the missing executable and say
  how to install it.
* An LLM-judged checker sits uneasily with D7 (stele never holds model
  credentials). In CI there is no agent to borrow. This is settled when that
  checker is built, not here.

## Rejected options

* **Go plugins or a public Go API (Option 2):** Go plugins only work on Linux and
  macOS, and need the exact same Go version and dependencies as the host. A public
  `pkg/` API forces paid plugins into Go and freezes our internals.
* **WebAssembly (Option 3):** a good sandbox, but most real plugins need the
  network or local tools like `semgrep`, which a sandbox takes away. It adds a
  runtime to the binary for a problem the exec model already solves.
* **No plugins (Option 4):** every paid parser would have to ship inside the free
  binary, and every new checker would wait for a stele release.

## References

* Git subcommands: `git` runs any `git-<name>` found on `PATH`.
* Terraform plugin protocol: https://developer.hashicorp.com/terraform/plugin
* Go `os/exec` and `PATH` lookups: https://pkg.go.dev/os/exec#hdr-Executables_in_the_current_directory
