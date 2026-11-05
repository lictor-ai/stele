# A second agent: Codex

* Status: proposed
* Date: 2026-11-04
* Amends: ADR-0005 ("the generated file is stele's own; no file of the
  user's is touched"), ADR-0009 ("a second agent adds a way to choose (a flag
  on the command it installs)": this is that flag, and the interface widens
  as ADR-0009 allowed), spec D62, D63, D68 and D69

## Context and Problem Statement

stele works with one agent, Claude Code (ADR-0004). #99 adds OpenAI's Codex
CLI, the way Claude Code works: the rules before a write, a check after it,
and the rules loaded when a session starts. What Codex does was measured on
its real CLI, 0.166.0, on 2026-11-04, and its payloads are recorded under
`cmd/stele/testdata/hook/codex`:

* Codex reads hooks from `.codex/hooks.json` in the shape Claude Code uses.
  It writes files with one tool, `apply_patch`, whose single patch can add,
  update, delete and move several files. Its shell tool is called `Bash`,
  but runs PowerShell on Windows.
* A `PreToolUse` deny is the same JSON as Claude Code's. Codex refuses
  `permissionDecision` "ask": a hook cannot ask the user.
* Codex runs a project's hooks only in a trusted project, and only once the
  user approved each one with `/hooks`. The approval is stored by the hook's
  position and a hash of its settings, so changing its command needs a new
  approval; until then `codex exec` skips it without a word.
* At start, Codex loads `AGENTS.md` (or `AGENTS.override.md`) from each
  directory from the project root down to where it runs, joined and capped
  at 32 KiB. That file is the user's: projects write their own instructions
  there.

So stele hook needs to know which agent it answers; the neutral flow needs
calls that touch several files, and an agent that cannot ask; and the rules
reach Codex through a file stele shares with the user.

## Considered Options

* **Which agent:** a flag on the installed command (`stele hook
  --agent=codex`), or reading it from the payload, whose fields the two
  agents mostly share.
* **The rules at start:** a section stele owns inside `AGENTS.md`; a file of
  stele's own that `AGENTS.md` would have to name; or the rules in the
  `SessionStart` hook's context, which Codex caps at 2,500 tokens.
* **stele's own files, with no ask:** refuse the write with a message for
  the agent, or let it through with a warning.

## Decision Outcome

Chosen: the flag, a marked section of `AGENTS.md`, and a refusal, because
each is explicit and safe: the flag costs nothing and cannot be fooled by a
payload's shape; the section is the one file Codex surely loads, with no
cap below the file's; and a write that could switch stele off is never let
through.

### Choosing the agent

* `stele hook --agent=<name>` answers that agent; with no `--agent` it
  answers Claude Code, so every hook installed before stays as it is. An
  unknown name says nothing, as any input stele cannot read.
* `cmd/stele/agents.go` holds the registry, the default first. What concerns
  every agent, whichever runs, goes over the whole registry: a write by one
  agent to another's settings files still asks.
* A rules file is written for each *active* agent under a root: one whose
  stele hooks are installed there, or whose rules file holds stele's rules.
  With none, the default agent is. A Claude Code project never gets an
  `AGENTS.md` it did not ask for, nor a Codex one a `.claude/`.

### One call, several files

* An `Event` carries `Edits`, one per file. Before a write, any of stele's
  own files among them asks (or refuses); otherwise each project decides
  once, over the rules of all its files, the same files again being the
  retry; a removal shows no rules. After a write, each file written is
  checked and the answers are one, within the message cap.

### An agent that cannot ask

* `Words.NoAsk` makes the ask message a refusal addressed to the agent, and
  the adapter renders an `Ask` as a deny.

### The rules in a section of a user's file

* The adapter splices its rendered rules between two marker lines in the
  file and changes nothing outside them; a file with no section gets one
  appended; markers out of order make the file foreign, never written.
  Uninstall removes the section only, and the file when nothing else is left.
* Rule blocks inside it are the blocks and the hash Claude Code's rules file
  uses (`internal/rulesblock`), so a session's record reads the same.
* That file is not one of stele's own: a write to it asks no one. An agent's
  edit inside the section is undone by the next hook, and `stele check`
  fails while it is out of date.

### Codex's hooks

* `stele init --agent=codex` writes three hooks into `.codex/hooks.json`:
  before `apply_patch` and the shell, after `apply_patch`, and at session
  start. Their command carries no version stamp: an upgrade of stele would
  otherwise need every hook approved again. A switch changes the command in
  place, keeping the hook's position, and init says to approve it again.
* Rules count as loaded only at a session's `startup`, and only when Codex's
  walk from the project root reaches root's `AGENTS.md`, with no
  `AGENTS.override.md` in its place and the section within the cap. A resume,
  compaction, clear or fork forgets what was loaded, until measured.
* Only `stele init` runs `codex --version`, and says what the version means;
  stele asks for 0.141 or later, 0.145 on Windows. The hook starts nothing.

### Positive Consequences

* A Codex project gets the same deny, check and switches as a Claude Code
  one, from the same neutral flow.
* A project can use both agents; the log tells their results apart.

### Negative Consequences

* stele writes into a file of the user's. The guarantee is that it writes
  only between its markers, and the round trip is exact.
* Codex skips hooks the user has not approved, silently in `codex exec`;
  stele can only say so at init.
* No version warning for Codex's hooks.
* Codex's shell runs PowerShell on Windows, which the shell reader must
  read too.
