# Rules at session start

Status: proposed, not built. Date: 2026-10-24.

## Why

The agent eval of 2026-10-24 (`test/functional/agent/results/20261024-161524`)
compared four arms on three hard tasks with 74 rules, 6 runs each, Sonnet 5.5.
The two that matter here (blocking violations only; the run's own summary
counted a non-blocking warning, which the grader no longer does):

| arm | turns | tokens | followed every blocking rule |
|---|---|---|---|
| rules in CLAUDE.md | 12.3 | 308k | 6/6 |
| stele | 19.8 | 645k | 5/6 |

stele's extra cost comes from the pre-write deny. A run hits 3.3 denies on
average. Claude writes files in parallel batches, so one deny makes it send the
whole batch again: stele runs write 11.3 files, CLAUDE.md runs 6.3. Every extra
turn reads the whole cached context again.

Rules that are already in the context cost nothing per turn. So stele should
send the rules once, when the session starts, and deny a write only for rules
the agent has not been sent.

## What changes

1. A new `SessionStart` hook. `stele init` installs it next to `PreToolUse`
   and `PostToolUse`. It answers with the project's rules as
   `additionalContext`.
2. The session state remembers which rules were sent at session start. Those
   rules count as seen by the main agent for the whole session, not only for
   the current turn.
3. `backstop.Decide` treats a rule sent at session start as already shown. A
   write needing only such rules is not denied.
4. Nothing else changes: the post-write check and the deny for rules that were
   not sent stay as they are.

## Facts about Claude Code this relies on

Check each one against the Claude Code hooks documentation before you build. If
one is wrong, stop and report back.

- `SessionStart` fires when a session starts, and again on `resume`, `clear`,
  `compact` and `fork`. Its payload carries `session_id`, `transcript_path`,
  `cwd`, `hook_event_name` and `source` (one of those five). There is no
  `prompt_id`, `agent_id` or file.
- `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}`
  puts the text in Claude's context. `claudecode.Render` already writes this
  shape for any event that has a `Context`.
- `SessionStart` does not fire for a subagent. A subagent has its own context
  and does not see the main agent's session-start text.
- **The size limit.** Claude Code passes any hook's `additionalContext` in full
  only up to 10,000 characters. Past that, Claude gets a file path and a
  2,000-character preview (`install.MaxMessage`, `internal/install/assets.go:55`).
  The test's `stele.md` is 15.9 KB, so the 74 rules do not all fit. See
  "Budget" below.

## Design

### The hook event

In `cmd/stele/hook.go`, `runHook` returns early when `ev.File == ""`, and sets
up its `recover` only after that return. Move the `File` check into the
write cases of the switch, below the `defer`, and add:

```go
case ev.Name == "SessionStart":
    a = sessionStart(ev)
```

so a panic in `sessionStart` is recovered like any other.

`claudecode.Parse` must keep `cwd` and `source` for this event: add a
`Source string` field to `Event`, and fill `Cwd` if `Event` does not carry it
yet.

`sessionStart(ev)`:

1. Find the root with `findRoot(ev.Cwd)` and stop with no answer when
   `!usesStele(root)`. Only the project at `cwd` gets primed. A subproject with
   its own `stele.md` keeps the deny for its rules.
2. Run `enforce.Load`, `loadKey` and `freshIndex`, the same way `beforeWrite`
   does. On an error, prime nothing and answer only a `Warning` of your own:
   do not use `failure(...)`. With `block_on_error` on, `failure` returns a
   deny and no warning, and a deny means nothing at session start.
3. Choose rules within the budget (below) and render them with a new template,
   `internal/install/assets/session-rules.tmpl`.
4. Write the answer, `claudecode.Answer{Context: text}`, to stdout.
5. Only when that write succeeded, replace `st.Primed` with the chosen rules
   inside `session.Update`. This is the reverse of `beforeWrite`'s order, for
   the same reason. A rule that is answered but not recorded would be denied
   again later, which costs turns but is safe. A rule that is recorded but
   not answered would never be shown, which is unsafe. If the update fails,
   leave the answer as it is: the deny still covers those rules.

Prime whatever the `backstop` policy is. Sending rules is delivery, not
enforcement, so `backstop: false` still gets them.

### Session state

Add one field to `session.State`:

```go
// Primed records the rules sent to the main agent at session start: rule
// id to a hash of the text it was sent. They stay in its context for the
// whole session, so the turn reset does not clear them. A rule whose text
// changed since is not primed.
Primed map[string]string `json:"primed,omitempty"`
```

The hash is of what the agent read: the rule's rendered block (its scope and
body). `Entry.BodyHash` is not enough: it covers the body only, so a widened
`scope` would keep its old hash, and the deny would be skipped on files the
agent was never told the rule covers. `backstop.Rule.Hash` (below) must be
computed the same way, from the same entry.

- Do not reset `Primed` when `PromptID` changes. It lasts for the session.
- Every `SessionStart` replaces it, whatever the `source`. After `compact` or
  `clear`, the earlier text may be gone from the context, so the new answer is
  the only thing that counts.
- If `stele.md` changes during the session, an edited rule (body or scope) no
  longer matches and is denied as usual. A new rule is not in `Primed`
  and is denied as usual too.

### backstop.Decide

Add the text hash to `backstop.Rule` (`Hash string`). In `Decide`, for the main
agent only (`in.AgentID == ""`), skip a rule when
`st.Primed[r.ID] == r.Hash`. A subagent ignores `Primed`.

Keep the change inside `Decide`, so it stays a table test. New cases:

- a primed rule with a matching hash: no deny;
- a primed rule whose hash changed: deny;
- a primed rule in a subagent: deny;
- a primed rule after a new `PromptID`: still no deny;
- some rules primed and some not: deny with only the ones not primed.

### Budget

The text must stay within `install.MaxMessage` (9,000 bytes) unless the size
check above shows that `SessionStart` allows more.

Choose the rules in this order, and stop adding at the first one that does not
fit:

1. blocking rules, in index order: rules with a `check` and without
   `blocking: false` (`internal/check/check.go:284-296`);
2. then every other rule, in index order: checked rules with
   `blocking: false`, and guidance-only rules (no `check`).

Rules that do not fit are not primed, and the deny shows them when the agent
first writes a file they apply to. That is today's behaviour, applied only to
the rules that did not fit.

End the text with one line saying how many rules were left out, so the agent
knows more exist: "N more rules are shown when you write a file they apply to."

### The template

`session-rules.tmpl`, in the same style as `block-message.tmpl`:

```
This project has rules, enforced by stele. Follow them in every change.
A rule with a scope applies only to the files it lists; one without applies to every file.
{{- if .Note}}

Note: {{.Note}}
{{- end}}
{{range .Rules}}
---
{{if .Scope}}scope: {{.Scope}}
{{end}}{{.Text}}
{{end -}}
{{- if .Left}}

{{.Left}} more rules are shown when you write a file they apply to.
{{- end}}
```

Check first whether `Entry.Body` already contains the scope. If it does, do not
print it twice.

The CLAUDE.md arm's header won with similar wording
(`test/functional/agent/run.go:146`).

### Install

In `internal/adapter/claudecode/settings.go`:

- add `"SessionStart"` to `events`;
- the matcher is per event: `Write|Edit|MultiEdit` for the two tool events,
  and none for `SessionStart`, so every source (including `fork`) is primed;
- `Install`'s check, one stamped handler per event, already makes `stele init`
  add the missing `SessionStart` group to an existing install. Add a test for
  that upgrade.

Update the message at `internal/install/install.go:126`, and every test that
expects `(PreToolUse, PostToolUse)`.

## Rules this must keep

- D38: write nothing to `stele.md` or `stele.json`. Do not write `CLAUDE.md`
  either; it belongs to the user.
- D40: read rules only from `index.json`, through `freshIndex`.
- D45: no external plugin runs. `compile.EnsureFresh` with `plugin.Builtins()`
  is the existing path; keep it.
- The hook always exits 0 and never panics out (`runHook`'s recover covers the
  new case).

## Spec and ADR

This changes how v1 delivers rules (ADR-0004: "delivered and checked by Claude
Code hooks", where rules are shown per write). Add a short ADR-0005, "Rules at
session start", based on `docs/adr/adr-template.md`, and a D-number in the v1
spec that points to it. Update `docs/architecture.md` where it describes the
hook events. Ask the user before you change ADR-0004 itself.

## Tests

Unit:

- `claudecode.Parse` on a `SessionStart` payload for each `source`;
- `Render("SessionStart", …)`;
- `backstop.Decide` table cases above;
- `sessionStart`: a panic is recovered, `block_on_error` on with a broken
  index gives a warning and no deny, a failed state update still answers,
  a widened scope un-primes the rule, no stele project, budget overflow (some rules left out, the
  "N more" line, only fitting rules in `Primed`), a `stele.md` parse error (the
  last good index is used and the note is shown), and `compact` replacing
  `Primed`;
- `hook` end to end: SessionStart, then a PreToolUse on a file whose rules all
  fit → no deny; on a file with a left-out rule → deny with only that rule;
- install: fresh install has three events; an old two-event install is
  upgraded by `stele init`; uninstall removes all three.

Functional (`test/functional`): extend `hook_pre_test.go` with the
SessionStart → write sequence.

## Measuring it

Rerun the agent eval with the same three scenarios, 6 runs per arm. The
`stele` arm gets the new hook through `stele init`. Add to `Result` in
`test/functional/agent/run.go`: `primed` (rules sent at start), `primed_bytes`
and `left_out`.

It is a success if the `stele` arm, compared with the 2026-10-24 run:

- averages ≤ 14 turns and ≤ 380k tokens;
- follows every blocking rule in at least as many runs as the CLAUDE.md arm;
- averages ≤ 1 deny per run.

If the 9,000-byte budget leaves many rules out in this test, report how many,
and the denies they caused, before tuning anything.
