# Agent A/B eval

Gives headless Claude Code, or Codex (`-agent codex`), the same coding tasks in several setups and grades each run with one yardstick. It measures what stele adds in time, tokens and cost, and what it buys in compliance.

**It spends real money.** Each run is one `claude -p` session, about $0.03 to $0.15 on Sonnet. The default (6 scenarios × 3 arms × 1 rep) costs about $1.

## Run

```sh
go run ./test/functional/agent                       # every scenario, arms none, claudemd, stele
go run ./test/functional/agent -reps 2 -parallel 3
go run ./test/functional/agent -scenarios go-logging -arms stele,stele-tools
go run ./test/functional/agent -agent codex          # Codex: arms none, agentsmd, stele
```

| Flag | Default |
|---|---|
| `-scenarios` | every directory in `scenarios/` |
| `-arms` | `none,claudemd,stele` |
| `-reps` | 1 |
| `-agent` | `claude-code`; or `codex` |
| `-model` | `claude-sonnet-5-5`; `gpt-6-luna` for Codex |
| `-effort` | `medium`: Codex's `model_reasoning_effort` |
| `-codex` | the npm package's `codex.js` (run with `node`), else `codex` on `PATH` |
| `-parallel` | 3 |
| `-timeout` | 10m per run |
| `-claude` | newest VS Code extension binary, else `claude` on `PATH` |
| `-stele` | built from this module |
| `-keep` | off: each run's temporary tree is deleted after grading, and its diff kept in `results/<time>/` |

## Arms

| Arm | Setup |
|---|---|
| `none` | the fixture, with every `stele.md` removed |
| `claudemd` | each `stele.md` pasted into a `CLAUDE.md` beside it |
| `agentsmd` | the same, into an `AGENTS.md`: the Codex counterpart of `claudemd` |
| `stele` | `stele init --agent=<agent>` and `stele compile`: the hooks in `.claude/settings.json`, or `.codex/hooks.json` and the rules in `AGENTS.md` |
| `stele-tools` | `stele`, plus a system-prompt line telling the agent to change files only with Write and Edit. Diagnostic: it shows what stele does when the hooks see every write. Not run by default. |

Each run starts from a fresh temporary copy of `scenarios/<name>/repo/`, committed to git. Claude runs with `--setting-sources project`, so your own settings and plugins stay out and the arms are comparable. stele's key lives in a per-run temporary directory.

### Codex

Each run is one `codex exec --json`, with the prompt on stdin. **It uses your ChatGPT quota.** Codex gets a `CODEX_HOME` of the eval's own, in a new directory in your user cache directory (not the temp directory, where Codex won't set up its helper binaries) that holds a copy of your `~/.codex/auth.json` and nothing else of yours, and is deleted when the eval ends. Your `~/.codex` is never changed.

- The project is trusted with `-c projects={...}`, for that run only.
- In the `stele` arm, the harness asks Codex for the hooks it found (`codex app-server`, `hooks/list`, no model call) and approves each with `-c hooks.state={...}`, as `/hooks` would. `codex exec` would skip them otherwise.
- The sandbox is `workspace-write` (`windows.sandbox="unelevated"` on Windows). Go's cache and temp files go to a directory added with `--add-dir`, so `go test` works.
- The run waits 3 s after setup: stele counts `AGENTS.md` as loaded only when nothing in the project changed in the 2 s before the session started (README, Limits, Codex).
- Turns are the model requests in the session's rollout; tokens come from `turn.completed`. Denies and blocks are counted in the rollout's tool outputs, which is also copied beside the stream. Time is wall time; Codex reports no cost.

## Results so far

[results/README.md](results/README.md) lists every experiment.

- **Before v1.0.0-dev.2, stele lost to rules in CLAUDE.md.** With 74 rules,
  compliance was 5/6 against 6/6, at twice the tokens. The cause was a deny
  before each new scope.
- **v1.0.0-dev.2 matches CLAUDE.md.** Over 9 scenarios, both arms were 100%
  compliant at 163k tokens a run.
- **stele beats CLAUDE.md when the task pulls against a rule.** In
  `probe-cli-marks` with Sonnet, CLAUDE.md was compliant in 5 of 8 runs and
  stele in 8 of 8, for 8% more tokens.
- **Open:** in `probe-local-style`, Haiku under stele read files one call at a
  time, which cost 63% more tokens.

## Probe scenarios

The `probe-*` scenarios look for rules that CLAUDE.md lets slip. Each sets
something against a rule: a habit, the code around the change, a repository
doc, or the task itself. They use the big scenarios' rulebook and skeleton.

## Big scenarios

The `big-*` scenarios are many times harder than the others. They share one
74-rule `stele.md` (16 KB, ~4k tokens), with about 45 regex rules, guidance, and
decoy scopes for web, scripts, CI, Docker, SQL and k8s. They also share a Go
skeleton (`internal/log`, `clock`, `id`, `api/respond.go`) that the rules point
the agent at.

| Scenario | Task |
|---|---|
| `big-todo-service` | concurrent store, five REST routes with validation, server with graceful shutdown, tests |
| `big-log-analyzer` | nginx log parser, percentile stats, CLI with flags, text and JSON output, exit codes, tests |
| `big-billing-refactor` | discount codes, regional tax and CSV export added to a legacy package with 4 old violations |

`task_ok` needs `go vet` and `go test` to pass. The guidance rules a regex
can't check (doc comments, table tests, ctx first, `MaxBytesReader`, FlagSet,
integer money...) are graded by heuristics into `guidance missed`. That count
does not affect `compliant`. A run costs roughly $0.3 to $2.

## Grading

Every arm is graded the same way. The final tree is copied, the fixture's `stele.md` files are put back, and `stele check --all` runs in each project. Each scenario also has its own checks in `grade.go`:

- `compliant`: no rule violations.
- `task_ok`: the task was done (the code builds, the function exists).
- `scope_creep`: lines outside the task were changed.

## Output

`results/<time>.jsonl` holds one line per run. `results/<time>.md` holds means per scenario and arm, and stele's differences against the other arms. `results/<time>/` keeps each run's stream and session transcript.

The reported counts:

- `denies`: writes stele paused to show rules.
- `blocks`: post-write violations, read from the session transcript.
- `hook bytes`: the text stele added.
- `bash writes`: shell commands that look like they wrote a file, which the hooks cannot see (README, Limits 2).
