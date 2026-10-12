# v1 is for one developer: rules by scope, checked by hooks

* Status: accepted
* Date: 2026-10-12
* Amends: the v1 spec, §2.1, §4 and §5

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

Everything already built stays and is used as designed: the parser, stable ids
in `stele.lock`, the index and `EnsureFresh`, session state, the trust rule for
`.stele/`, and signed generated files (D57).

### What moves to the "team" milestone

The MCP server and its three tools (spec §4, #9), the matcher prompt (#18),
logging rule selections (#11), the plugin registry and exec protocol (#14), and
`stele status` and `stele push` (#17). ADR-0002 and ADR-0003 stand: they are the
design for that milestone, and nothing in v1 closes it off.

### What changes in the spec's hook (§5)

The hook no longer arms on a turn and disarms on `fetch`; there is no `fetch`. It
blocks a write that would bring rules not yet shown this turn. The three-blocks
circuit breaker is gone with it: the agent completes the exchange by reading the
block reason and retrying, which needs no cooperation beyond that. `backstop` and
`block_on_error` keep their meaning: `backstop` off means the hook never blocks,
and `block_on_error` decides whether a failure of stele itself blocks or lets the
write through with a warning.

### Positive Consequences

* No model call and no network on any path: the hook reads a local, signed
  index. It is fast and deterministic, and testable from recorded hook input.
* The agent cannot skip the exchange: the rules arrive in the deny reason of the
  very write that needs them.
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

## Rejected options

* **Option 1** solves a team's problem first. For one developer with one file it
  adds an MCP server, a subagent, two prompts and a failure policy, and delivers
  nothing Option 2 does not.
* **Option 3** gives the agent every rule on every turn, whatever it is doing,
  and finds violations only in CI. It is what stele exists to improve on.
