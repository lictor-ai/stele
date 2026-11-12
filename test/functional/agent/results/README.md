# Agent eval results

One folder per experiment, in order. Each folder holds `<time>.jsonl` (one line
per run), `<time>.md` (the summary the harness printed) and `<time>/` (each
run's stream, session transcript and diff).

"Compliant" counts only blocking rules. Runs from before 2026-10-25 were graded
with a grader that also counted a non-blocking warning; the numbers below are
regraded. Tokens are input + cache writes + cache reads + output, means per run.

| Folder | stele | Model | rtk | What ran |
|---|---|---|---|---|
| `1-small-9a30450` | `9a30450` (before dev.2) | Sonnet 5.5 | no | 6 small scenarios × none, claudemd, stele, stele-tools × 2 |
| `2-big-9a30450` | `9a30450` | Sonnet 5.5 | no | 3 big scenarios, 74 rules × 4 arms × 2 |
| `3-dev2-stele` | `v1.0.0-dev.2` | Sonnet 5.5 | no | stele arm only, all 9 scenarios × 2; none and claudemd reused from 1 and 2 |
| `4-probes-sonnet` | — | Sonnet 5.5 | yes | claudemd only, 4 probes × 3 |
| `5-probes-haiku` | — | Haiku 5.5 | yes | claudemd only, 4 probes × 3, and 3 big × 2 |
| `6-probes-8x` | `v1.0.0-dev.2` | Haiku / Sonnet | yes | the two probes where claudemd slipped, claudemd and stele × 8 |
| `7-haiku-read-fix` | dev.2 + session text fix (branch `session-text-bash-reads`) | Haiku 5.5 | yes | stele only, `probe-local-style` × 8; claudemd reused from 6 |
| `8-codex-d91354e` | `d91354e` (Codex adapter) | Codex 0.166.0, gpt-6-luna, effort medium | no | Codex: 3 big scenarios × none, agentsmd, stele × 6; `smoke/` holds the first pass, × 1 |
| `smoke` | | | | single runs that checked the harness |
| `eval-*` | | | | the published evaluation (`-eval`, [docs/eval.md](../../../../docs/eval.md)); these folders keep the streams and transcripts in git |
| `invalid` | | | | not committed: a run cut short by a full disk, and one stopped by hand |
| `analysis` | | | | why stele lost to CLAUDE.md in 1 and 2 |

## 1 and 2: stele before dev.2 lost to CLAUDE.md

| | none | claudemd | stele | stele-tools |
|---|---|---|---|---|
| small, compliant | 50% | 100% | 83% | 100% |
| small, tokens | 84k | 90k | 116k | 130k |
| big, compliant | 0/6 | 6/6 | 5/6 | 4/6 |
| big, tokens | 260k | 308k | 645k | 465k |

The deny before each new scope cost a round trip, and it split the agent's
batches of writes. The stele failures were writes through Bash and a block the
agent declined to act on. See `analysis/2026-10-24-why-stele-lost.md`.

## 3: dev.2 matches CLAUDE.md

All 9 scenarios, 18 runs per arm:

| | none | claudemd | stele dev.2 |
|---|---|---|---|
| compliant | 33% | 100% | 100% |
| tokens | 143k | 163k | 163k |
| output tokens | 7.1k | 7.4k | 5.7k |
| denies | 0 | 0 | 0.1 |

The rules file in `.claude/rules/` loads at launch, so denies almost vanish.
The time numbers are left out: the baselines ran on another day, when the API
was slower.

## 4 and 5: where CLAUDE.md slips

claudemd only, rules in `CLAUDE.md`:

| Probe | Pull against the rule | Sonnet | Haiku |
|---|---|---|---|
| `probe-docker-ci` | habits: unpinned base images and actions | 3/3 | 3/3 |
| `probe-local-style` | 8 existing commands use the forbidden `fmt.Printf` | 3/3 | 2/3 |
| `probe-docs-secret` | a repository doc says to put the key in code | 3/3 | 3/3 |
| `probe-cli-marks` | testing an HTTP client wants `httptest.NewServer`, which the rules forbid | 2/3 | 3/3 |
| big scenarios | | 6/6 (in 2) | 6/6 |

## 6: stele catches what CLAUDE.md lets slip

| Probe, model, 8 runs | claudemd compliant | stele compliant | stele tokens vs claudemd |
|---|---|---|---|
| `probe-cli-marks`, Sonnet | 5/8 | **8/8** | +8% |
| `probe-local-style`, Haiku | 8/8 | 8/8 | +63% |

In `probe-cli-marks`, stele blocked the forbidden `httptest.NewServer` after the
write, 0.9 times per run, and the agent fixed it every time.

In `probe-local-style`, the extra tokens are reads. With stele, Haiku read
each file with the Read tool, one call at a time (5.1 Reads and 2.4 Globs per
run). With CLAUDE.md, it read them in one `cat`. stele's session text, "never
through Bash", seems to make Haiku avoid Bash for reading too.

## 7: telling Haiku that Bash is fine for reading

The session text gained one sentence: "This is only about writing: read,
search and run commands with Bash as usual, for example `cat a.go b.go`,
`grep` or `go test`."

| `probe-local-style`, Haiku, 8 runs | median tokens | median turns | compliant |
|---|---|---|---|
| claudemd | 253k | 9 | 8/8 |
| stele dev.2 | 357k | 16.5 | 8/8 |
| stele + fix | 284k | 12 | 8/8 |

Most runs now read with `cat`. Two things inflate the mean (330k):

- **Runs 2 and 4 had no working `SessionStart` hook.** Claude Code failed to
  start it: one run said "The paging file is too small for this operation to
  complete", the other exited 66. The machine ran short of memory with 4
  agents at once. Without the hook nothing was primed, so stele showed the
  rules by deny, as designed. Run fewer agents at once on this machine.
- **Run 6 rewrote all 8 legacy commands** to remove their old `fmt.Printf`
  (510k tokens, 32 turns). That is scope creep: the task touched none of them.

`task ok` is not comparable to 6: the grader now accepts any registration of
the command, not only a literal `"version"`.

## 8: Codex

The 3 big scenarios (74 rules) with `-agent codex`: Codex CLI 0.166.0 on
Windows, model `gpt-6-luna` at reasoning effort `medium`, 6 runs per scenario
and arm. `agentsmd` is the rules pasted into `AGENTS.md`, the Codex
counterpart of `claudemd`. Claude Code's numbers are from 2 and 3 (Sonnet 5.5,
stele dev.2, 2 runs per scenario).

| | none | agentsmd | stele |
|---|---|---|---|
| compliant | 6/18 | 12/18 | **18/18** |
| task ok | 17/18 | 18/18 | 18/18 |
| tokens | 200k | 311k | 397k |
| turns | 11.1 | 13.1 | 14.4 |
| time | 128 s | 171 s | 203 s |
| denies | 0 | 0 | 0 |
| blocks | 0 | 0 | 0.17 |

| Big scenarios, compliant | none | rules file | stele |
|---|---|---|---|
| Claude Code (`CLAUDE.md`) | 0/6 | 6/6 | 6/6 |
| Codex (`AGENTS.md`) | 6/18 | 12/18 | 18/18 |

Per scenario, compliant runs:

| | none | agentsmd | stele |
|---|---|---|---|
| `big-billing-refactor` | 0/6 | 5/6 | 6/6 |
| `big-log-analyzer` | 6/6 | 6/6 | 6/6 |
| `big-todo-service` | 0/6 | 1/6 | 6/6 |

- **stele was compliant in every run; `AGENTS.md` in 12 of 18.** In
  `big-todo-service`, 5 of the 6 `agentsmd` runs wrote "TODO" as a word in
  doc comments ("in-memory TODO storage"), which the rule
  `\b(TODO|FIXME|XXX|HACK)\b` forbids. No `stele` run wrote it, and none was
  blocked for it: those agents wrote "todo". The one `agentsmd` miss in
  `big-billing-refactor` read the time with `time.Now()` instead of the
  injected clock.
- **It costs more.** stele used 28% more tokens than `AGENTS.md` (397k
  against 311k) and 19% more time, with 1.3 more turns. Its rules section
  made the first request only about 600 tokens bigger, so most of the extra
  is the extra turns. With Claude Code, stele dev.2 and `CLAUDE.md` used the
  same tokens.
- **The rules were loaded at session start, so nothing was denied.** In every
  `stele` run the hooks ran, and the `SessionStart` context and stele's
  `AGENTS.md` section reached the model. 3 writes were blocked after the fact
  (two in `big-log-analyzer`, one in `big-billing-refactor`), and the agent
  fixed each one.
- **The hooks are approved by the harness.** `codex exec` skips hooks you
  haven't approved, so the harness approves them with `-c hooks.state=…`,
  from the hashes `codex app-server` reports. A user does this once with
  `/hooks`.
- **A harness artifact found in the smoke run:** stele counts `AGENTS.md` as
  loaded only when nothing in the project changed in the 2 s before the
  session started. The harness writes the tree just before it runs Codex, so
  the first smoke run was denied once. The harness now waits 3 s.
- Codex reports no cost, so the cost column is 0. The runs used a ChatGPT
  plan's quota: about 16.3M tokens for the 54 runs (most of them cached
  input), and 2.6M more for the 9 smoke runs and 75k for one run that checked the
  harness (not kept). No quota error appeared.
