# The agent adapter interface: everything Claude Code-specific in one package

* Status: accepted
* Date: 2026-10-27
* Amends: architecture §`internal/adapter` ("there is no `Adapter` interface
  yet; `cmd/` calls it directly. The interface comes with a second agent.")

## Context and Problem Statement

v1 supports one agent, Claude Code (ADR-0004), and the architecture left the
`Adapter` interface for the second one. Meanwhile Claude Code's shape has
spread outside `internal/adapter/claudecode`:

* `cmd/stele` calls `claudecode` about 80 times: its `Event` and `Answer`
  types, its hook event names (`PreToolUse`, `SessionStart`, …), its tool
  names (`Write`, `Edit`, `MultiEdit`, `Bash`), its settings and rules file
  paths.
* The priming logic in `cmd/stele/hook_session.go` encodes when Claude Code
  loads `.claude/rules`: at launch, resume, compaction, for which subagent
  types, from which directory. That is one agent's behaviour, measured on one
  release, sitting in the agent-neutral flow.
* `internal/install` installs Claude Code's hooks by name, and its messages
  name Claude Code's tools and settings files.

Codex, Cursor and others come in v2. Each loads rules differently, names its
tools differently, and configures hooks in its own file. If the Claude Code
knowledge stays spread out, every new agent means an edit through the whole
hook flow, and a change for one agent can break another. Where should the
line be, and should it be drawn now, before a second agent exists?

## Considered Options

* **Wait for the second agent**, as the architecture planned: design the
  interface when its shape is known from two examples.
* **Draw the line now**: an `adapter.Adapter` interface in `internal/adapter`,
  agent-neutral `Event` and `Answer` types, and every Claude Code fact behind
  it in `internal/adapter/claudecode`. Only one file in `cmd/stele` names
  `claudecode`.

## Decision Outcome

Chosen option: **draw the line now**, because the leak is growing with every
issue (#57, #68 and #71 each added Claude Code knowledge to `cmd/`), and
moving it later means untangling it from more code. The interface is shaped by
what the neutral flow needs from an agent, not by guesses about Codex: a v2
adapter may widen it.

### The line

Agent-neutral, outside every adapter: finding the root, the index and
compile, scope matching, the backstop's decisions, session state, checks,
the guard's list of stele's own files (`stele.md`, `stele.json`,
`stele.lock`, `.stele/`), and the policy (`block_on_error`, the switches'
meaning).

Agent-specific, inside an adapter, and nowhere else:

* **The hook protocol**: parsing what the agent sends into a neutral `Event`,
  rendering a neutral `Answer` into what it reads back. Event names and tool
  names do not leave the adapter: the neutral event says what is happening
  (a tool is about to touch a file, has written one, a shell command is about
  to run, a session or subagent starts), whether the tool writes, and whether
  it replaced the whole file.
* **Install**: the agent's settings file, the hook entries and their
  switches, finding and removing stele's hooks, and the version they carry.
* **The rules file the agent loads at launch**: its path, its rendering, the
  hash of a rule's block, whether a file there is stele's, its size limits.
* **What the agent loaded**: given a session-start event and a root, whether
  the agent starting now loaded that root's rules file, for which agent id,
  and whether this is a launch. The when-and-where of loading (launch,
  resume, compaction, subagent types, the directory it was launched in) is
  measured per agent and per release, and lives with that agent.
* **Files that configure the agent**: which files, if changed, would change
  the agent's hooks or settings, so the guard asks before they are written.
* **Words**: what the agent's tools are called in a message ("the Write and
  Edit tools", "Bash"), what its settings files are called, and the session
  context, which is instructions about this agent's tools.

### Rules

* `internal/adapter` defines the interface and the neutral types. It imports
  no adapter.
* Only `internal/adapter/<agent>` and one registry file in `cmd/stele`
  import an adapter package. A test fails the build otherwise.
* `stele hook` uses the Claude Code adapter. The installed hook command does
  not change in v1, so no project has to run `stele init` again. A second
  agent adds a way to choose (a flag on the command it installs).
* Behaviour does not change: the refactor moves code, and every existing test
  passes unchanged in what it checks.

### Positive Consequences

* A v2 agent is a new package and a registry line, not an edit through the
  hook flow.
* The priming conditions, which are measured facts about Claude Code, can
  change (such as priming a session launched in a subdirectory, measured on
  2026-10-27) without touching the neutral flow.

### Negative Consequences

* An interface designed from one implementation: the second agent may need
  to widen it.
* A large move of code with no change a user can see.
