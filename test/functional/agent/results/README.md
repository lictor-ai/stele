# Agent eval results

The published results are on the
[`eval-results` branch](https://github.com/lictor-ai/stele/tree/eval-results/test/functional/agent/results),
at this same path: every experiment, including the runs where stele lost. Its
[README](https://github.com/lictor-ai/stele/blob/eval-results/test/functional/agent/results/README.md)
lists them. That branch has no history in common with `main`, so a clone of
`main` doesn't carry them.

A run of the harness on your machine writes here (`-out <name>` writes
`<name>/`). Everything here but this file is ignored by git.

To rerun `-regrade`, `-rescrub` or `-baseline` against the published results,
check that branch out beside this repository and pass its folders:

```sh
git fetch origin eval-results
git worktree add ../stele-eval-results origin/eval-results
go run ./test/functional/agent -regrade ../stele-eval-results/test/functional/agent/results/eval-v1.5.0-pilot/20261112-170804
```

To publish a new eval, commit its `results/<name>/` folder to the
`eval-results` branch at this path, and add one line in
[docs/eval.md](../../../../docs/eval.md) linking to it.
