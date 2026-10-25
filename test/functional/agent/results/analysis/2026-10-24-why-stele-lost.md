# Why stele lost to CLAUDE.md on small runs

Data: round 1 (6 small scenarios × 4 arms × 2, `20261024-124718`) and round 2 (3 big
scenarios, 74 rules, × 4 arms × 2, `20261024-161524`). Sonnet 5.5. Transcripts:
round 2 only (round 1's were deleted when the disk was cleaned).

## Failures in the stele arms

| Run | What happened | Root cause |
|---|---|---|
| go-logging, stele #1 and #2 | `fmt.Println` written with a Bash heredoc. No hook fired. | Hooks see only Write and Edit. CLAUDE.md does not care how a file is written. |
| big-billing, stele #2, stele-tools #1 and #2 | The agent edited `Compute`'s return line and kept its legacy `time.Now()`. stele blocked it. In its final summary, the agent said it would not fix it because "fixing it would mean adding a clock parameter, which would break the existing callers". | The rule arrived after the design was settled. The block text, "fix these lines now, without changing code outside your task", invites treating the call as legacy. Every CLAUDE.md run kept the signature and used `clock.Real{}.Now()`. |

## Where stele's cost comes from (round 2, means per run)

| | CLAUDE.md | stele |
|---|---|---|
| API messages | 7.3 | 12.3 |
| messages that write | 1.8 | 5.0 |
| writes per message | 3.45 | 2.27 |
| context per message | 40k | 49k |
| denies | 0 | 3.3 |
| output tokens thrown away in denied writes | 0 | ~2.8k |

1. **One deny per scope.** Each new scope (todo, api, tests, cmd) costs a round trip,
   and each round trip re-reads about 45k tokens. In 4 of 6 stele runs, the agent had
   already read stele.md with `cat` before its first write. Those denies were pure
   overhead.
2. **Batches break up.** With rules in CLAUDE.md, the agent writes the whole project in
   1–2 parallel batches. Under stele it writes in 5 smaller ones. That gives about 5
   more messages, which accounts for most of the 2× tokens.
3. **Denied content is generated twice.** A denied Write has already produced the
   whole file, and the retry produces it again.
4. **The batch window re-denies the real retry** (round 1, migrations: 2 denies for 1
   file in all 4 runs).

## Denies rarely change the code

Of 37 writes that were denied and then retried:

- **20 retries were identical** to the denied write. The deny text says "retry the
  same write", and the agent did exactly that.
- **2 retries fixed** a violation a regex can see.
- **15 retries changed** in ways no regex rule detects.

The deny guarantees the agent sees the rules before a write lands. It does not get
them in front of the agent before the code is written, and that is the moment that
matters.

## What this says about the design

Rules shape design: inject the clock, parse flags in `run()`, respond through the
helpers. An agent plans first and then writes in batches. CLAUDE.md puts the rules
in context at planning time. stele puts them in front of the agent at write time,
which is too late to change the plan and costs a round trip per scope.

For stele to beat CLAUDE.md on small runs:

- **Delivery must happen at planning time and cost no round trip**, for example as
  context at the start of the turn. That makes it equal to CLAUDE.md on cost.
- **stele must win on what CLAUDE.md cannot do:**
  - check every change, including Bash writes, for example on the files git sees as
    changed;
  - refuse to let the agent finish with blocking violations;
  - word blocks so a violation on a line the agent touched is not treated as legacy.
