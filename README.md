# stele eval results

This branch holds the published results of stele's agent eval: every
experiment's summaries, results lines, diffs and setups, and, for the
`eval-*` folders, the scrubbed streams and transcripts. They are kept here so
that a clone of `main` doesn't carry them.

This branch has no history in common with `main`. Never merge it into `main`,
or `main` into it. The files sit at the same path as on `main`
(`test/functional/agent/results/`), so links keep the same sub-path.

- What the experiments are: [test/functional/agent/results/README.md](test/functional/agent/results/README.md)
- The evaluation and how to run it: [docs/eval.md on main](https://github.com/lictor-ai/stele/blob/main/docs/eval.md)
- The harness: [test/functional/agent on main](https://github.com/lictor-ai/stele/tree/main/test/functional/agent)

## Adding a new eval

1. Run it on `main`: `go run ./test/functional/agent -eval ... -out <out>`.
   It writes `test/functional/agent/results/<out>/`, which `main` ignores.
2. Copy that folder to the same path on this branch, add a row to
   `test/functional/agent/results/README.md`, and commit here.
3. On `main`, add one line in `docs/eval.md` linking to
   `https://github.com/lictor-ai/stele/tree/eval-results/test/functional/agent/results/<out>`.

To rerun `-regrade`, `-rescrub` or `-baseline` against these results, check
this branch out beside a clone of `main`:

```sh
git fetch origin eval-results
git worktree add ../stele-eval-results origin/eval-results
go run ./test/functional/agent -regrade ../stele-eval-results/test/functional/agent/results/eval-v1.5.0-pilot/20261112-170804
```
