# stele — Concepts and Open Questions

**Status:** discussion document, not a spec. Its job is to give us a shared vocabulary and a shared list of what we still have to argue about.

**How to read it.** §1–§2 are the problem and the map. §3 pins the words down. §4–§7 carry the reasoning. §9 is the state of the argument — every decision and every open question, one line each, with a pointer to where it is argued. Open questions have stable IDs, so cite them as `O-SPAWN`, `O-TAGS` rather than by section number.

---

## 1\. What stele is

A single Go executable that makes coding agents actually respect a project's rules.

The problem it solves is **not** that rules are missing. Teams already write them — in `CLAUDE.md`, in `AGENTS.md`, in wikis, in review comments. The problem is that a real ruleset is far too large to sit in an agent's context, and the parts that do sit there get buried under file reads and tool output long before the agent starts writing code.

So stele's job is **selection and timing**: put the *small relevant subset* of the rules in front of the agent at the *moment it matters*.

Two things shape everything else:

- The value of the tool is bounded by the quality of the matching between a task and its relevant rules. That is the hard problem. Everything else is plumbing.  
- Matching needs structure — a description, tags, a path scope — and the prose a team already has does not carry it. Both routes are open to us, and D36 says both can be present at once. What is not settled is whether deriving is good enough to be the *default* (`O-ENTRY`).

**Out of scope, deliberately:**

- **Sourcing rules from outside the repo** (Notion, Confluence, wikis). A real problem, not ours. We assume rules are materialized as files on disk.  
- **Authoring the rules.** The user brings them. We organize and deliver them.

---

## 2\. The shape of the system

One pipeline, split across two paths that have completely different economics.

COLD PATH — runs rarely, offline, nobody is waiting

─────────────────────────────────────────────────────────────────────

    source rules ──── compile (§6) ────► derived index

    the team's prose                     matchable form

                                               │

══════════════════════════════════════════════ │ ═══════════════════

HOT PATH — runs every turn, someone is waiting │

                                               │

    agent explores, forms an intent            │

              │                                │

              ▼                                ▼

         payload (§4) ──────────────────►  matcher (§5)

    brief \+ situated context              a subagent holding

                                          the whole index

                                               │

                                               ▼

                                        selected rules

                                               │

                                               ▼

                                    the agent, about to write code

**The split is the load-bearing idea.** Everything corpus-wide, expensive, or agent-assisted happens on the cold path, where nobody is waiting for it. The hot path never touches the corpus except to *select* from it — it does not process it, derive from it, or reorganize it. Small deterministic work on the current task's own files is allowed, which is what `O-CTX-RES` proposes; model-assisted work and anything that persists are not. When we are tempted to add work to the hot path, the first question is whether it could have been done at compile time instead.

---

## 3\. Vocabulary

| Term | Meaning |
| :---- | :---- |
| **rule** | One guideline the team maintains, as prose. |
| **corpus** | All of a project's rules. |
| **source rules** | The prose the user writes and owns. stele never edits these. |
| **derived index** | stele's compiled, matchable rendering of the corpus. A build artifact. |
| **compile** | The cold-path pass from source rules to derived index. |
| **brief** | The agent's declaration of what it is about to do. Intent, in its own words. |
| **situated context** | What the agent has already seen, travelling alongside the brief — paths, extracts, possibly file contents. |
| **payload** | Everything one tool call hands over. What that is, is `O-INPUTS`. |
| **matcher** | The subagent that reads the derived index and the payload and returns the relevant rules. |
| **selection** | What the matcher returns. |
| **hot path / cold path** | Per-turn work vs. rare offline work. See §2. |
| **backstop** | The hook that fires on first file write when the agent did not call the tool. |
| **re-arm** | When the backstop becomes eligible to fire again within a session. |

---

## 4\. Delivering the rules

Two delivery paths, because neither is sufficient alone.

### 4.1 The tool call

stele exposes a tool. The agent calls it once it has explored the repo and knows what it intends to do. What that call carries is §4.3 — it is the least settled question in this section, and it is where the matcher's whole input comes from.

**The tool may be called more than once in a turn.** A turn's intent is not fixed at the moment it is first declared. An agent twenty edits in may discover the change also needs a migration, and a payload that was accurate when it was assembled is now incomplete. Nothing about *when* the first call happens fixes that — only a second call does. So a call is a correction point rather than a one-shot, and a half-formed first call costs a mediocre selection rather than the whole turn.

**The weakness of this path is obvious:** the agent is *invited* to call the tool, not obliged to. It may skip it, or reach the code before it has called at all.

### 4.2 The hook backstop

Hence the second path. A hook fires the first time the agent writes to a file in a turn — the deterministic moment where "exploring" becomes "coding." It fires whether or not the agent cooperated, and that is what makes the tool path trustworthy.

**The hook blocks and redirects:** it stops the write and tells the agent to call the stele tool first.

**What is blocked is the write, not the exploring.** An agent that reaches a write early is being told that code cannot land before rules are delivered — not that it must declare an intent it has not formed yet. If it cannot yet say what it is about to do, the intended response is to explore further and call then. We cannot compel that; the message can only make the option explicit. But a redirect that reads as *call this instant* will be obeyed literally, thin brief and all, so the wording is doing real work here.

**Rejected: the hook asks whether the agent has finished exploring** and branches on the answer — redirect if yes, stand aside if no. It is aimed at a real case, the agent that writes before it understands the task, but it does not fix it. If "not finished" lets the write through, code is written with no rules at all, which is worse than the thin selection it was meant to avoid. If "not finished" still blocks the write, that is the unconditional block we already have. And it asks the agent to report a fact we can observe: a brief is self-reported only because intent is unobservable (§4.3), whereas *the agent wrote a file* is the signal itself. The early write is better handled where it actually hurts — by the wording above, by `O-REARM`, and by the tool being callable again (§4.1).

This imposes a hard dependency: it only works on agents whose hooks can block a tool call. Agents whose hooks cannot block fall into the same bucket as agents with no hooks at all — tool path only, no backstop.

> **`O-REARM` — the re-arm policy.** What re-arm defends against is **intent drift**: a brief covers the part of the task the agent had understood when it called, and a long turn grows past it. So the live question is less *how often* than *on what signal*. Counters — once per turn, every N edits — are the crude form. Scope-change signals are the interesting one: a write into a new package or directory, a new file type, the first edit after a run of reads. Too eager and it becomes noise the user disables; too lazy and a long turn drifts far from the rules it was given at the start. Note the division of labour with §4.1: the voluntary re-call covers drift the agent notices itself, and re-arm exists to catch the drift it does not.

### 4.3 What the payload carries

Because §4.2 redirects rather than injects, everything funnels through a single tool call and there is exactly one payload to design. Its argument list is the matcher's entire view of the turn.

> **`O-INPUTS` — what the matcher is given to match on.** The tool's argument list is this question. What each candidate uniquely carries:  
> 

> - **The brief** — the agent's declaration of what it is about to do, in its own words. It carries *intent*, and the argument for it is that intent is not otherwise observable: a tool cannot read the conversation, it sees only the arguments it is handed, and before any code is written there is no diff and no edit — the thing a rule would be checked against does not exist yet. It is also the input we cannot verify; the agent may write a thin brief or a wrong one, and we would never know.  
  >   
> - **The user's request, verbatim** — the same intent, unmediated. The brief is the agent's reading of what was asked; this is what was asked. It is the only input that survives the agent misunderstanding the task, which is exactly the case where getting the right rules in front of it matters most. Two problems. It is often the *less* informative of the two — "fix the flaky test" says nothing the agent has not since worked out for itself — and on turns where the agent is continuing work rather than answering a new request, it does not exist at all. Mechanically it has the same shape as the read ledger in `O-CTX-SOURCE`: a hook on the user's turn captures it at zero parent-token cost with nothing self-reported, which puts it on the unportable half of §4.4. Asking the agent to relay it instead reintroduces precisely the paraphrasing it exists to avoid.  
  >   
> - **The target path** — the file about to be written. On the backstop path (§4.2) the hook intercepts a write and knows it exactly; on the tool path the agent names it. Objective, costs nothing, and a strong scoping signal — path globs are how most existing rule systems scope, `.cursor/rules` included. Carries no intent whatsoever: the same path takes a refactor, a bugfix and a docs change.  
  >   
> - **Surrounding code** — the files the agent has already read. Objective, because stele resolves them itself rather than trusting a description of them. Carries *idiom*: the sibling handler shows that this project wraps every handler in the same middleware and has a house error type. How it reaches us is `O-CTX-SOURCE`; at what resolution, `O-CTX-RES`.  
  >   
> - **The turn's diff** — code already written. The objection is obvious: the tool is meant to fire *before* the agent writes anything, so there should be nothing to diff. In practice there are three sources, and only the first is genuinely unavailable early. **Edits since an earlier call in the same turn** — D11 makes calls 2..n ordinary, and §4.2 blocks only the *first* write, so by the time of a re-call the agent has been editing freely for a while. **A re-arm** — `O-REARM` fires the hook again mid-turn, on a write into a new package say, and redirects to the tool with substantial work already behind it. **A working tree that was already dirty when the turn began** — uncommitted changes from earlier turns of the same session, or the user's own edits, are there on the very first call and cost nothing to read. In any session past its opening turn that last one is usually non-empty, which makes the diff considerably more available than "before code is written" suggests.  
  >   
  > The caveat is what it actually tells us. A diff is evidence of what the session has been doing, not of what the agent is about to do next — a proxy for intent rather than a source of it. It is also the natural input for exit-time verification (§8.1), a different problem we have deferred. Using it here bets that recent work predicts imminent work.  
  >   
> - **What was already selected this turn** — the rules an earlier call delivered. Less a matching signal than a de-duplication one: without it every re-call re-derives and re-delivers the same three rules and the agent pays for them twice.  
  >   
> - **Repo-level facts** — language, framework, layout, the package manifest. Real signal, but static per project, which is an argument for baking it into the index at compile time (§6) rather than shipping it on every call. Listed here to be moved, not carried.

>   
> **The axis underneath the list is declared vs. observed.** Most of these stele can obtain for itself and therefore trust; the brief is the only one that is purely self-reported. That would weld *carries intent* to *cannot be verified* — except for the user's request, which carries intent and, captured by a hook, is observed rather than declared. It is the one candidate that breaks the weld, and that is an argument for taking it seriously despite being the thinner of the two intent sources. It also means an observed-only design — path, reads, diff, captured request, no agent cooperation anywhere — is coherent. But such a design no longer needs the tool path at all, which puts it in conflict with D4, so anyone arguing for it has to reopen §4.2 as well.  
>   
> **More inputs is not better**, which is what makes this a selection problem rather than a budget one. `O-CTX-RES` notes the dilution directly: feed a matcher a lot of code and it starts keying on incidental features of it.

**Rejected: the agent pastes code into the tool call.** It spends the most expensive tokens in the system — parent output tokens — on every single turn, and agents are unreliable at it. Asked to include what it read, an agent paraphrases, truncates, or includes three of the eight files. The result is a lossy summary of code, which is just a longer brief carrying the same self-reported weakness. Whatever we do here, we pass *references* and resolve them ourselves.

> **`O-CTX-SOURCE` — how situated context reaches us.** Two candidates, which compose rather than compete:  
> 

> - **The agent passes paths, stele reads them.** References are cheap to emit — a path list is a handful of tokens where the files are thousands — and stele reads the content itself, so the content is objective even though the choice of paths remains declared.  
> - **A hook maintains a read ledger.** A hook on the agent's file reads accumulates a per-session set of what it actually looked at. The tool then already knows, at zero parent-token cost, with nothing self-reported. Two costs: it sits on the unportable half of §4.4, and it is noisy — an agent reads plenty it does not care about, so the ledger needs recency or relevance weighting rather than being handed over whole.

>   
> The likely answer is the ledger where hooks allow it, with declared paths as the portable floor.  
>   
> **`O-CTX-RES` — at what resolution.** Full file contents are the obvious answer and probably the wrong default. They put the matcher's per-turn cost squarely in the range `O-COST` warns about, and they dilute precision — feed a matcher a lot of code and it starts keying on incidental features of it, surfacing the concurrency rules because a file it was shown spawns goroutines, on a turn that was a docs change.  
>   
> The cheaper candidate is **extracts**: imports, package, exported signatures, sibling filenames, whether a test file exists. Small, deterministic, computable without a model, and carrying most of the stack-and-idiom signal. Full contents become a budgeted escalation rather than the default.

**How we should settle all three:** ship the cheapest set that could plausibly work — brief and target path — log the read ledger alongside it (§7), and check afterwards whether the inputs we left out would have changed the selection. Every question in this subsection is measurable, and we should measure them rather than argue them now.

### 4.4 Portability

stele must work beyond Claude Code.

**Tools are the portable half.** Every serious coding agent can call an external tool, so the primary path is available more or less everywhere.

**Hooks are the unportable half.** Every agent has a different hook model, some have none, and of those that have one, not all can block a tool call — which §4.2 requires. We need a thin per-agent adapter layer, and we should accept up front that the guarantee degrades on some agents: tools only, with the backstop unavailable.

> **`O-AGENTS`** — which agents are in scope for the first version, and what we do on agents whose hooks cannot block or do not exist.  
>   
> **`O-INSTALL`** — what `stele init` installs per agent, and how we uninstall cleanly.

---

## 5\. Matching

**This is the core of the tool and the least settled part of the design.** Given a task and a corpus of rules, which rules are relevant?

**A subagent does the matching.** The reasoning is context isolation. If the parent agent does the selecting, the parent has to see the whole corpus to choose from it — reintroducing exactly the context bloat we exist to prevent. A subagent can read all two hundred rules and return the three that matter, so **the parent's context only ever contains the selection.** That property is worth the round trip on its own.

Two secondary benefits: the matcher gets a prompt tuned for selection rather than for coding, and the same matcher serves both delivery paths.

> **`O-SPAWN` — how the subagent gets spawned, mechanically. *(gates the architecture)*** A tool cannot spawn a subagent by itself; it returns a value and that is all. The candidates: the tool's response instructs the parent to dispatch one; stele shells out to the agent's own CLI in headless mode; or we lean on a protocol facility for asking the client to run a completion on our behalf, where one exists and is actually implemented. Each has a different portability story. **Resolved for v1 by D12** — the tool's response instructs the parent to dispatch one — with what that costs in portability recorded in the spec's known weaknesses. It stays listed here because the answer for agents with no subagent facility is still open.  
>   
> **`O-CORPUS` — full corpus or pre-filtered candidate set.** Full corpus is simplest and best. It stops scaling at some corpus size we have not measured. A pre-filter — by path globs, by tags — restores scaling and introduces a way to silently miss a rule.  
>   
> **`O-COST` — latency and cost.** This runs on effectively every turn. A slow or expensive matcher is a tool people uninstall.  
>   
> **`O-BUDGET` — a token budget on the selection**, and what happens at the ceiling. Truncate, rank, or tell the agent the budget was exceeded?  
>   
> **`O-EMPTY` — what "no rules apply" looks like.** It must be a cheap, unembarrassing answer, or the matcher will invent relevance to seem useful.

See `O-INPUTS` in §4.3 — what the matcher receives is decided there, along with its two sub-questions `O-CTX-SOURCE` and `O-CTX-RES`.

---

## 6\. Compiling and maintaining the rules

The user brings prose written for humans. Matching needs structure. **stele closes that gap itself** — we do not push it onto the user. That is unsettled, and `O-ENTRY` below is where it is argued rather than assumed; this section describes the machinery it commits us to.

source rules  ────  stele compile  ────►  derived index

whatever prose the   occasional,          descriptions, tags, path scopes,

user maintains,      offline, out of      whatever matching needs

wherever they keep   the hot path

The user edits the left side. stele owns the right side. The index is a build artifact.

**stele never holds model credentials and never picks a provider.** It borrows the intelligence of the agent the user is already running — on the cold path here, and on the hot path in §5. Compile runs *inside* the user's agent session: stele supplies the inputs — the rule text, the current tag vocabulary, the schema it wants back — the agent performs the derivation, and stele validates and writes the result. The user reviews the diff and commits it. The intelligence is borrowed from the agent the user is already paying for. That fixes the division of labour; how the procedure is packaged and invoked is `O-COMPILE-PKG`.

### The line we should not cross

Derived artifacts regenerate freely. **The substance of a rule is never auto-edited.**

Rules are governance artifacts. The moment an agent can rewrite its own constraints as a side effect of being constrained by them, the tool stops being worth installing. A change to what a rule *says* must be an explicit, reviewable, committed act by a human.

**The narrow exception is bookkeeping.** stele stamps an `id:` onto a rule that has none — from the binary rather than from a model, deterministically, never touching the rule text, and always as a diff the user reviews and commits. This is not the hazard above: it is not a model, and it does not change what any rule requires. It is necessary because identity has to live in the file the user owns. Held anywhere else it dies on a fresh clone or diverges between developers, and derived from the rule's own text it resets on exactly the edit §7 exists to prompt. Argued in the v1 spec under D20.

This also rules out **reorganizing rules mid-turn**: unreviewable, nondeterministic, and it races across concurrent sessions.

### Open

> **`O-ENTRY` — is the derived floor good enough to be the default entry path? *(upstream of most of this section)*** One half of this is settled and one is not, and they were tangled together for longer than they should have been. **Settled: both authored and derived metadata can be present** (D36). §1 requires it, and where a source already carries a description written by someone who knows what the rule means, we read it rather than re-derive it. That is what stops this being an architecture gate — the design carries both regardless of which one people actually use. **Open: whether deriving is good enough to be the default**, which is an empirical claim rather than an argument, and is why §9.2 now files it under settle-with-data. The document assumes stele derives, and most of §6 exists only under that assumption. Two questions bear on it.  
>   
> *Is the derived floor good enough?* Deriving from prose is what keeps entry cost near zero, which matters because the payoff for authoring is largely illegible — a better description *might* get a rule surfaced more often, and nobody can ever see the rule that was not surfaced. The claim is not that teams will not do authoring work; they write Semgrep rules and ESLint plugins. It is that stele cannot show them what the work bought. That premise is testable rather than assumed, and two things test it. Count how many real repos already carry `.cursor/rules` or `.mdc` files: those *are* the authored format, and every team holding one has already paid the migration voluntarily. And §7's *offered, never selected* and *never offered at all* make a bad rule visible, which is most of what the illegibility argument claims is impossible.  
>   
> *What happens where both exist?* A `.cursor/rules` file already carries a description and a scope written by someone who knows what the rule means — a matching-quality argument, not a cost one, and §5 is where the value of the tool actually sits. So we read it rather than re-derive it. What we owe an answer on is precedence: whether a recompile may overwrite a hand-written description, and how a correction survives one. That is `O-METADATA`.  
>   
> What the requirement costs us. A format we *defined* would dissolve `O-GRAIN`, `O-RULE-ID`, `O-COMPILE-PKG`, `O-COMPILE-WHEN`, `O-INDEX-VCS` and `O-TAGS`, because the user answers each of them by writing the file. **v1 took exactly that route** and all six duly collapsed. The cost is the one this paragraph predicts: the team has to author. So the six are not answered in general — only deferred behind a requirement that derivation will drop again.  
>   
> **`O-GRAIN` — what is one rule.** §3 says a rule is one guideline, but the sources we want to read are not organized that way: a `CLAUDE.md` holds dozens of guidelines in one file, and a `.cursor/rules` file holds one or several. So either a file is a rule — contradicting §3, and coarse enough that the parent receives forty guidelines to get the three that applied — or compile splits files, and we owe an answer on what it splits on. Headings and blank lines are deterministic and wrong on real prose. Model judgment is accurate and unstable across runs, which runs straight into `O-RULE-ID`.  
>   
> This is upstream of more than it looks. `O-BUDGET` budgets a selection in tokens, `O-CORPUS` asks where the corpus stops scaling, and every signal in §7 counts offers per rule. All three assume a unit that this question defines.  
>   
> **`O-RULE-ID` — where a rule's identity comes from, and whether it survives recompile.** §7 logs rule identifiers, and every signal there tracks one rule across many turns. If compile derives rules from prose, an identifier has to be stable across re-derivations of the same source, or each compile silently resets the history D10 exists to accumulate. Content hashes are stable until someone fixes a typo. Path plus ordinal is stable until a rule is inserted above. Something durable and human-legible — a slug the derivation proposes once and then keeps — is likelier, and still needs an answer for what happens when a rule is reworded past recognition.  
>   
> **`O-RULE-LOC` — where source rules live, and in what formats we read them.** Two halves.  
>   
> *Location.* Do we impose `.stele/rules/**.md`, or accept a configured set of globs and let people keep rules where they already are — including adopting `.cursor/rules` or sections of an existing `CLAUDE.md`? Imposing a location is simpler for us; accepting globs is what §1's no-migration constraint points at.  
>   
> *Format.* Markdown is the assumption, but the sources worth reading are not plain markdown. `.mdc` files carry frontmatter we would have to read or deliberately ignore, and a section of a `CLAUDE.md` is not a file at all. Whether we accept a fixed set of formats, a set of per-source adapters, or everything as opaque prose that compile deals with, is unsettled — and entangled with `O-GRAIN`.  
>   
> **`O-METADATA` — under `O-ENTRY`: how hand-written metadata and derived metadata coexist.** §1 requires that both can be present, so the question is no longer whether to allow authoring but what happens where the two meet. Fully derived is zero friction and gives the user no way to correct a bad derivation, which is why a correction path exists at all.  
>   
> Precedence is the unsettled half. If a hand-written description always wins, a recompile can never improve it and a stale correction outlives the rule it corrected. If the derivation always wins, the correction path is decorative. Something in between — the index records which fields were authored and leaves those alone, and compile reports where it would have disagreed — is likelier, and needs a representation in the index that `O-INDEX-VCS` has to carry and a human has to be able to read in a diff.  
>   
> **`O-TAGS` — the tag vocabulary.** Almost certainly user-defined and open rather than a taxonomy we impose, since an imposed taxonomy fits nobody. But an open vocabulary sprawls — past roughly thirty tags it becomes a second corpus that itself needs selecting. Some approval step for new tags is probably necessary. Do we ship a starter vocabulary or an empty one?  
>   
> **`O-COMPILE-PKG` — how compile is packaged and invoked.** Three candidates:  
> 

> - **A work order the binary emits.** `stele compile` prints its inputs and the schema it wants back, and the agent reads them off stdout. It runs on any agent that can execute a command, and it can never disagree with the binary about the schema, because the binary that is running produced it.  
> - **An installed skill.** A procedure file `stele init` writes, invocable by name by the user or reached for by the agent from its description — which would answer part of `O-COMPILE-WHEN` as a side effect. It is the native form for a procedure with checkpoints, which is what compile becomes if new tags need approval (`O-TAGS`) or a bad split needs correcting (`O-GRAIN`).  
> - **Both, with a seam between them.** The skill owns invocation, the step sequence and the user checkpoints; everything that varies with the stele version stays a call back into the binary. Where that seam falls is then the substance of the question.

>   
> Two costs pull against the installed form. It sits on the unportable half of §4.4, so the work order has to exist anyway for agents with no skill mechanism. And it puts stele's own derivation logic in a file the agent can edit — a weaker version of the hazard D8 exists to prevent, and worth naming here, since this section's whole argument is that governance artifacts should not be rewritable by the thing they govern.  
>   
> **`O-COMPILE-WHEN` — when compile runs.** Manually, on a git hook, or automatically when stele notices the index is stale relative to source?  
>   
> **`O-INDEX-VCS` — whether the index is committed.** Committing means the team shares one derivation and reviews changes to it; it also means merge conflicts in a generated file.  
>   
> **`O-REORG` — periodic reorganization.** Incremental compile handles changed rules cheaply. Vocabulary drift — three tags that mean the same thing, one tag doing too much work — needs a corpus-wide pass. Separate command, run rarely, output reviewed as a diff.

---

## 7\. Logs

stele records what happens on every turn. This is cheap to build and it is **the only way we will ever find out whether the matching works.**

Per call — and a turn may hold several (§4.1) — roughly: the brief, which rules were offered, which were selected, how many tokens were spent. Rule identifiers (`O-RULE-ID`) and short text only — no source code, no diffs. If we log the read ledger of §4.3 to evaluate situated context, it is file paths only, on the same terms. Local, append-only, never leaves the machine.

We should not ship a log we never read; that is just disk usage with good intentions. The signals we know are in there:

| Signal | What it means |
| :---- | :---- |
| **Offered, never selected** | The rule keeps surfacing as a candidate and the matcher keeps passing it over — its scope is too broad, or its description is misleading. |
| **Never offered at all** | A dead rule. It exists in the repo and has never once reached an agent. |
| **Always selected together** | Two rules are one rule, or one implies the other. |
| **The briefs themselves** | Every selection pairs a real task description with a rule that turned out to apply. Over enough turns this is a corpus of how work is actually described *in this repo* — exactly the input needed to improve the descriptions the matcher keys on. |

Several of these are pure counting — no model, no compile step, deterministic. Those can ship early and work immediately.

### Open

> **`O-LOGSIG` — which signals we act on, and whether stele applies a change or only proposes one.** Proposing is consistent with never auto-editing source rules. Proposing is also easy to ignore.  
>   
> **`O-LOG-LOC` — where logs live and whether they are shared.** Per-developer local is the obvious default. Team aggregation would give far better statistics and raises questions we should not answer yet.  
>   
> **`O-LOG-VOLUME` — the small-sample problem.** Under a few dozen turns these statistics are noise, and anything we build on them has to say so rather than making confident claims on tiny samples.  
>   
> **`O-ENTRENCH` — the entrenchment hazard.** If we ever rank candidates by past selection, past selection becomes self-fulfilling and a rule that was never picked never gets picked again. Any ranking scheme needs an escape hatch.

---

## 8\. Deferred

### 8.1 Exit-time verification

When the agent finishes a turn, stele checks that what it just did actually complies with the rules.

**The approach we have in mind:** a dedicated subagent receives the turn's diff, with the relevant rules already in its context window, and returns the problems it flagged to the main agent, which decides what to do about them.

The appeal is **independence**. The agent that just wrote the code has motivated reasoning about whether that code is compliant; a subagent holding only a diff and a set of rules has no stake in the answer. It is also the same shape as the matcher in §5 — one mechanism, two uses.

**We are deferring it.** It is a genuinely different problem — it has a diff to work from, and it needs a notion of severity. We would rather ship the pre-code path and learn from it. The approach above is a starting point for that later discussion, not a commitment.

Known weaknesses to take up when we return:

- **A diff may not be enough context.** The subagent sees changed lines, not the files they live in. Plenty of violations are invisible at that resolution: a function that duplicates one three hundred lines above it, a new handler that skips a pattern every sibling handler follows, a removed guard whose necessity is only legible from the surrounding code. Passing whole files means more context and more cost, and past some diff size it stops being an option at all. Where that line sits is open.  
- **Rules written as prose make poor audit instruments.** "Prefer composition over inheritance where it improves testability" is fine guidance and useless to check against. This will likely mean each rule eventually carries a second, question-shaped rendering. We should keep the rule format open enough to add that later without a migration.

### 8.2 Not deciding yet

- Anything about blocking a turn on a compliance failure.  
- Fetching rules from sources outside the repo.  
- Team-level log aggregation.  
- **Whether some rules should be checked by running a command** (a linter, `semgrep`) rather than by consuming agent context. Likely a strong idea — the fastest way to improve matching is to have less to match against — but it is a separable concern.

---

## 9\. State of the argument

**Where the numbers live.** D1–D11 and D36 are discussion-level decisions and are recorded here. D12–D35 are v1 implementation decisions and live in `docs/superpowers/specs/2026-08-29-stele-v1-design.md`, which is authoritative for the first release. §9.2 below remains the discussion-level state — the spec closes or defers most of it for v1, and says which.

### 9.1 Decided

| \# | Decision | Where |
| :---- | :---- | :---- |
| D1 | Rules are files on disk. We do not source them from wikis, and we do not author them. | §1 |
| D2 | Pre-code delivery is the focus. Exit-time verification is deferred. | §8.1 |
| D3 | Two delivery paths: a tool the agent calls, and a hook backstop. | §4 |
| D4 | The hook blocks and redirects to the tool rather than injecting rules itself. | §4.2 |
| D5 | The agent never pastes code into the tool call. We pass references and resolve them ourselves. | §4.3 |
| D6 | A subagent does the matching. The parent's context only ever holds the selection. | §5 |
| D7 | stele never holds model credentials and never picks a provider. It borrows the agent the user is already running — compile on the cold path, the matcher on the hot path. | §6, §5 |
| D8 | stele never edits the substance of a rule. It may stamp bookkeeping metadata onto a rule that has none — deterministically, from the binary, as a reviewable diff. Derived artifacts regenerate freely. | §6 |
| D9 | No reorganizing rules mid-turn. | §6 |
| D10 | Per-call logs are recorded: local, append-only, identifiers and short text only. | §7 |
| D11 | The tool may be called more than once in a turn. A call is a correction point, not a one-shot. | §4.1 |
| D36 | Authored and derived metadata can both be present. Where a source already carries a description, we read it rather than re-derive it. Numbered out of sequence because D12–D35 are in the v1 spec. | §1, §6 |

### 9.2 Open

Grouped by what they block, not by section.

**Gates the architecture**

Answered for v1 by D12. The row stays because that answer does not generalise to agents with no subagent facility.

| ID | Question | Where |
| :---- | :---- | :---- |
| `O-SPAWN` | How does the matcher subagent get spawned, on each target agent? | §5 |

**Needed before a first version**

| ID | Question | Where |
| :---- | :---- | :---- |
| `O-INPUTS` | What is the matcher given to match on? Defines the tool's argument list. | §4.3 |
| `O-GRAIN` | What is one rule, when a source file holds many? Does compile split, and on what? | §6 |
| `O-AGENTS` | Which agents do we support first, and what happens where hooks cannot block? | §4.4 |
| `O-INSTALL` | What does `stele init` install, and how do we uninstall cleanly? | §4.4 |
| `O-RULE-LOC` | Where do source rules live, and in what formats do we read them? | §6 |
| `O-RULE-ID` | Where do rule identifiers come from, and do they survive a recompile? | §6 |
| `O-REARM` | On what signal does the hook re-arm, given that intent drifts within a turn? | §4.2 |
| `O-EMPTY` | What does "no rules apply" look like? | §5 |
| `O-CORPUS` | Does the matcher see the full corpus or a pre-filtered candidate set? | §5 |
| `O-COST` | What latency and cost is acceptable for a per-turn matcher? | §5 |
| `O-BUDGET` | Is there a token budget on the selection, and what happens at the ceiling? | §5 |
| `O-METADATA` | Under `O-ENTRY`: how do hand-written and derived metadata coexist, and which wins on recompile? | §6 |
| `O-COMPILE-PKG` | Is compile a work order the binary emits, an installed skill, or both? | §6 |
| `O-COMPILE-WHEN` | When does compile run, and how is staleness detected? | §6 |
| `O-INDEX-VCS` | Is the derived index committed to the repo? | §6 |
| `O-TAGS` | Starter tag vocabulary or empty, and how are new tags approved? | §6 |

**Settle with data once we are shipping**

| ID | Question | Where |
| :---- | :---- | :---- |
| `O-ENTRY` | Is the derived floor good enough to be the default entry path? Empirical, and the architecture does not depend on the answer. | §1, §6 |
| `O-CTX-SOURCE` | Under `O-INPUTS`: how does surrounding code reach the matcher — declared paths, read ledger, or both? | §4.3 |
| `O-CTX-RES` | Under `O-INPUTS`: at what resolution — full file contents or cheap extracts? | §4.3 |
| `O-LOGSIG` | Which log signals do we act on, and do we apply or only propose? | §7 |
| `O-LOG-LOC` | Where do logs live, and are they ever shared? | §7 |
| `O-LOG-VOLUME` | How do we avoid confident claims on tiny samples? | §7 |
| `O-ENTRENCH` | How does a ranking scheme avoid entrenching past selections? | §7 |
| `O-REORG` | How and when does corpus-wide reorganization run? | §6 |

