# v1 is for one developer: rules by scope, checked by hooks

* Status: accepted
* Date: 2026-10-12
* Amends: the v1 spec, §1, §2.1, §2.3, §3 (`scope`), §4, §5, §6 and §7

## Context and Problem Statement

The spec's v1 chooses rules with a model. The hook blocks the first write of a
turn and tells the agent to call `stele.request`. A matcher subagent reads every
rule through `stele.corpus` and picks the relevant ones, and `stele.fetch` hands
them over and disarms the hook. That design exists for one problem: a team whose
rules no longer fit in an agent's context, so something has to choose.

The first user is one developer with one `stele.md`. Their need is narrower:
stele reads the file locally, and the agent follows it. For that, a model-driven
matcher, an MCP server and a subagent are cost without benefit. They are also the
parts of v1 hardest to get right: two highly sensitive prompts, a three-call
exchange the agent must complete, and a circuit breaker for when it does not.

What makes an agent follow a rules file is simpler:

1. it sees the rules that apply, at the moment it writes code;
2. it hears about a violation right after it makes one, and fixes it;
3. CI catches what slipped through.

Claude Code's hooks can do the first two without a model. A `PreToolUse` hook
that denies a write shows its reason to the agent, which reads it and retries. A
`PostToolUse` hook can send text back after a write, with `decision: "block"` and
a reason. The payload carries `prompt_id`, so "once per turn" needs no extra
state.

## Considered Options

* **Option 1:** keep the spec's v1: model-driven selection through an MCP server
  and a matcher subagent.
* **Option 2:** v1 for one developer. Rules are chosen by their `scope`. A
  `PreToolUse` hook shows them before a write, a `PostToolUse` hook checks the
  write, and `stele check` covers CI. Model-driven selection waits for teams.
* **Option 3:** no hooks; put `stele.md` in `CLAUDE.md` and rely on CI.

## Decision Outcome

Chosen option: **Option 2**.

### What v1 does

* **Before a write.** `stele hook` runs on `PreToolUse` for Write and Edit. The
  rules for the target file are every rule whose `scope` matches its path,
  relative to the root, plus every rule with no `scope`, which applies
  everywhere. If the write brings rules the agent has not been shown this turn,
  the hook denies it and the reason lists those rules in full. The agent reads
  them and retries; the next write goes through. Rules shown are recorded in the
  session state per session, and a new `prompt_id` starts a turn with none shown
  (#10).
* **After a write.** `stele hook` runs on `PostToolUse` for Write and Edit. It
  runs the checks of the rules that apply to the file, the built-in `regex`
  checker only, on that file only. A blocking violation goes back to the agent as
  `decision: "block"` with file, line, rule and message. The write is not undone;
  the agent fixes it (#29).
* **In CI.** `stele check` as the spec describes in §8 (#15), without sinks.
* **Install.** `stele init` registers the two hooks and creates `stele.md` and
  `.stele/` (#12). There is no MCP server and no subagent to register.

Everything already built stays: the parser, stable ids in `stele.lock`, the
index and `EnsureFresh`, session state, the trust rule for `.stele/`, and signed
generated files (D57). Two of them change role, below.

### How the hook works, in detail

* **Which files.** The project root is found from the written file's own path,
  walking up as architecture §6 describes, not from the payload's `cwd`, so a
  file in a subproject with its own `stele.md` gets that subproject's rules. A
  file outside any root gets no rules. Paths are matched against `scope` relative
  to the root, with `/` separators on every platform.
* **Keeping the index fresh.** With no `request`, the hook itself calls
  `compile.EnsureFresh` with the built-in sources only (D45), so an edit to
  `stele.md` takes effect on the next write without a manual `stele compile`. The
  hook therefore does read rule content, unlike the spec's §5 hook.
* **"Shown" is per session, per agent, per turn.** The hook applies to subagents
  too: there is no matcher subagent to stand aside for. A subagent has its own
  context, so rules shown to the main agent do not count as shown to a subagent,
  or the other way round. The record is keyed by `session_id` and `agent_id`
  (empty for the main agent), and reset by a new `prompt_id`. A missing
  `prompt_id` simply widens "turn" to the whole session.
* **Parallel writes.** The agent often issues several writes in one message, and
  their hooks run before it reads any deny reason. So for a short window after a
  deny (a few seconds, shorter than any model round trip), every write that needs
  the same rules is denied too. The agent reads the rules once and retries the
  whole batch.
* **Save before deny.** The record of rules shown is saved before the deny is
  returned. If it cannot be saved, the hook follows `block_on_error` rather than
  deny the same write forever; there is no circuit breaker to stop such a loop.
* **A broken `stele.md`.** When `stele.md` stops parsing, the hook keeps using
  the last good index, which is still signed and still verified, and tells the
  agent that `stele.md` has an error at line N. Rules stay enforced while the
  file is being edited. With no good index at all, the failure policy below
  applies.
* **After a write, only the agent's changes.** For an Edit, only violations in
  the lines its new text covers are sent back. For a Write, the whole file is the
  agent's. Older violations elsewhere in the file are left to `stele check`, so
  the agent is not pushed into rewriting code outside its task.
* **Session state** gains the record of rules shown, per agent. `Armed`,
  `Touched` and `Blocks` belonged to the arm-and-fetch exchange and go; there is
  no re-arming per directory, since a write into a new directory brings that
  directory's scoped rules anyway.

### What moves to the "team" milestone

The MCP server and its three tools (spec §4, #9), the matcher prompt (#18),
logging rule selections (#11), the plugin registry and exec protocol (#14), and
`stele status` and `stele push` (#17). ADR-0002 and ADR-0003 stand: they are the
design for that milestone, and nothing in v1 closes it off.

### What changes in the spec's hook (§5)

The hook no longer arms on a turn and disarms on `fetch`; there is no `fetch`. It
blocks a write that would bring rules not yet shown this turn. The three-blocks
circuit breaker is gone with it: the agent completes the exchange by reading the
block reason and retrying, which needs no cooperation beyond that. The hook also
no longer stands aside inside subagents, and it reads rule content (above).

`backstop` keeps its meaning: off, the hook never blocks. `block_on_error` changes
meaning: in the spec it covers the matcher failing and the exchange not
completing; in v1 it covers stele itself failing with no good index to fall back
on (no key, a session that cannot be saved, `stele.md` broken before it ever
parsed). Off, the default, lets the write through with a warning; on, blocks and
says why. A broken `stele.md` with a good index behind it is not a failure: the
last good rules apply.

### Positive Consequences

* No model call and no network on any path: the hook reads a local, signed
  index. It is fast and deterministic, and testable from recorded hook input.
* For a Write or Edit, the agent cannot skip the exchange: the rules arrive in
  the deny reason of the very write that needs them, batch included.
* Violations of checkable rules are caught at the write, not only in CI.
* v1 loses its two hardest deliverables, the matcher prompt and the three-call
  exchange, without closing the door on them.

### Negative Consequences

* Selection is only as good as `scope`. A rule with no `scope` is shown for every
  file, so many unscoped rules mean long block messages. The README says to keep
  unscoped rules few.
* Rules are chosen by path, not by task. A rule about SQL with no `scope` is shown
  when the agent edits a README; a rule scoped to `**/*.go` is not shown for a
  migration written as `.sql`. Model-driven selection fixes this, for teams.
* The agent is shown the rules and told about violations, but can still ignore
  what a regex cannot catch.
* The hooks see Write and Edit only. A file written through Bash (`cat > f`,
  `sed -i`) or NotebookEdit gets no rules and no check; `stele check` in CI is the
  only catch there. A model denied a Write may fall back to Bash.
* `scope` changes meaning: in the spec it is "a hint to the matcher, not a hard
  filter". In v1 it is the filter, so a broad scope shows a rule more often and a
  narrow one hides it. §3 says so.

## Rejected options

* **Option 1** solves a team's problem first. For one developer with one file it
  adds an MCP server, a subagent, two prompts and a failure policy, and delivers
  nothing Option 2 does not.
* **Option 3** gives the agent every rule on every turn, whatever it is doing,
  and finds violations only in CI. It is what stele exists to improve on.
