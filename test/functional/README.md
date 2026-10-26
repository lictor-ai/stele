# Functional tests

Black-box tests of the `stele` binary. `TestMain` builds `cmd/stele`. Each test
makes a throwaway git repository with its own signing key, then drives `init`,
`compile`, `check` and `hook` with the payloads Claude Code sends. Nothing here
imports stele's packages.

```sh
go test ./test/functional/ -count=1 -v      # ~90 s
go test ./test/functional/ -short           # skips the 2000-file check
```

| File | What it challenges |
|---|---|
| `install_test.go` | init/uninstall keep the user's settings; `--remind` and `--check` written, kept and refused; the `backstop` note; broken settings.json is refused; D38; lock ids survive reorder, reword, insert, delete; invalid rules fail check |
| `hook_pre_test.go` | once per turn, per agent, per session; parallel batches; fast retries inside the 3 s window; subprojects and nested `.git`; path spellings that try to dodge a scope; every README glob; recompiling on a `stele.md` edit; broken `stele.md` with and without a good index; `block_on_error`, `init --remind=off`; garbage, 70 MiB and hostile payloads |
| `hook_post_test.go` | exact line:col; CRLF; BOM; Unicode columns; Edit checks only new lines (with and without a patch, bogus patch, duplicated text); replace_all; MultiEdit; the 20-violation and 10,000-char caps; binary, >32 MiB, deleted, symlinked files; `(?m)`; invalid rules; `init --check=off` |
| `check_test.go` | changed vs `--all` vs `--base`; deleted, renamed and Unicode paths; lock missing or stale in a fresh clone; JSON contract; subprojects skipped; no git; 2000 files |
| `security_test.go` | tampered `index.json`; forged or foreign-key session state; no plugin and nothing inside the repository ever runs (planted `git`, `stele-*`); probes of an agent disarming stele through its own writes |
| `perf_test.go` | hook latency cold and warm for 3, 50 and 500 rules; recompile in the hook; post-write on 10 KiB to 16 MiB; bytes and estimated tokens the hook injects |

A line starting `FINDING` in `-v` output is behaviour worth knowing that the spec
does not forbid. Set `STELE_FT_MEASURE` to a file path to save the measurements there as JSON; `results/` keeps one such file per stele measured.

The agent A/B eval, with a real model, is in [agent/](agent/).
