# stele

stele makes Claude Code follow the rules in your repository's `stele.md`:

- **Before a write**, the agent is shown the rules for the file it is about to write.
- **After a write**, the lines it just wrote are checked against rules that have a regex check.
- **In CI**, `stele check` checks the changed files.

It is one Go binary. It uses no model and makes no network calls.

## Install

Needs Go 1.27 or later.

```sh
go install github.com/lictor-ai/stele/cmd/stele@latest
```

Or, from a clone: `go install ./cmd/stele`. For a private module, set `GOPRIVATE` and git credentials first.

The hook runs the bare command `stele`, so `$(go env GOPATH)/bin` must be on the `PATH` that Claude Code sees.

## Set up a repository

Run `stele init` in the directory where you start Claude Code, normally the repository root:

```
$ stele init
stele init in /home/you/project
created .stele/.gitignore
created stele.md
added stele hooks (PreToolUse, PostToolUse) to .claude/settings.json
```

- It works on the current directory only, and says so if a parent directory looks like the repository root (not when the current directory is a git worktree or submodule).
- It adds `stele hook --installed=<version>` to `.claude/settings.json` on `PreToolUse` and `PostToolUse` (matcher `Write|Edit|MultiEdit`, timeout 10 s). The rest of the file is kept. `<version>` is the release tag, or `devel` for a build from a commit (`go install ./cmd/stele` in a clone, or `@latest` before a tag); a build from a commit never replaces a release's stamp.
- It creates `stele.md` only if there is none. stele never writes `stele.md` or `stele.json` after that.
- Running it again prints `up to date, nothing written`.

Then write your rules in `stele.md` and run `stele compile`:

```
$ stele compile
3 rules: 0 matched by text, 0 by position, 3 new, 0 dropped
wrote stele.lock and .stele/index.json
```

**Commit** `stele.md`, `stele.lock`, `.claude/settings.json`, and `stele.json` if you have one. **Don't commit** `.stele/`; it ignores itself.

If a rule can't be used — a `check` other than `regex`, a pattern that doesn't compile or matches empty text, a `blocking` that isn't `true` or `false` — `stele compile` names it and exits 1. It still writes `stele.lock`, so the rule keeps its id, and the hook enforces the other rules and tells you once a session.

The hook also recompiles when `stele.md` changes, so edits take effect on the next write. Commit the updated `stele.lock`.

To remove stele: `stele uninstall`. It removes stele's hooks and `.stele/`, and keeps `stele.md`, `stele.lock` and `stele.json`.

## Writing stele.md

Rules are separated by a line holding only `---`. A rule may start with `key: value` lines; then a blank line, then the rule in plain prose. HTML comments are skipped.

```markdown
scope: ["**/*.go"]
check: regex
pattern: fmt\.Print(f|ln)?\(
message: log through internal/log, not fmt.Print

Log through the shared logger in internal/log. Never print with fmt.Print,
Printf or Println.

---

scope: migrations/

Never edit a migration that has been merged. Add a new one instead.

---

check: regex
pattern: AKIA[0-9A-Z]{16}

Never put credentials in code, config or tests. Read them from the environment.
```

- Rule 1 is shown before writes to Go files, and checked.
- Rule 2 has no `check`: it is guidance, shown before writes under `migrations/`.
- Rule 3 has no `scope`: it is shown and checked for every file.

| Key | Meaning |
|---|---|
| `scope` | Path globs: `["a", "b"]` or one bare value. No scope means every file. |
| `check` | `regex`, the only checker. Any other value is an invalid rule. |
| `pattern` | A Go (RE2) regular expression, for `check: regex`, taken as written: quotes are part of it, so `pattern: "net/http"` matches `"net/http"` with its quotes, as in an import, not a bare mention of net/http. |
| `message` | What a violation says. Default: the start of the rule. Taken as written, quotes included. |
| `blocking` | `true` (the default with `check`) or `false`. |

Scope globs:

- `**` matches any number of directories: `**/*.go`.
- A pattern with no `/` matches the file name at any depth: `*.go`.
- A leading `/` anchors at the root: `/*.go`.
- A trailing `/` matches everything under a directory: `migrations/`.
- `{a,b}` matches either: `src/**/*.{go,ts}`.
- A leading `!` excludes: `["internal/**/*.go", "!**/*_test.go"]`. Order does not matter, and a scope of only exclusions means every other file. To match a file whose name starts with `!`, write `\!`.
- On Windows and macOS, matching ignores case.

## What the agent sees

- **Before a write.** The first write in a turn to a file with rules the agent hasn't seen this turn is denied, with those rules as the reason. The agent reads them and retries. Each rule is shown once per turn, to the main agent and to each subagent.
- **After a write.** A blocking violation in the lines just written is sent back as a block, with file, line, rule and message. The write is not undone; the agent is told to fix it. Non-blocking violations are added as context.
- **A broken `stele.md`.** The last good rules keep applying, and the agent is told about the error.

## CI: stele check

| Command | Checks |
|---|---|
| `stele check` | Files changed against `HEAD`: staged, unstaged and untracked |
| `stele check --base <ref>` | Files changed since the merge base of `<ref>` and `HEAD` |
| `stele check --all` | Every tracked file |
| `stele check <path>...` | Exactly these files |

Add `--format json` for JSON. Deleted files are skipped.

`stele check` first checks the rules: `stele.md` must parse, every rule must be valid, and `stele.lock` must be committed and current. Then it checks the files:

```
$ stele check
main.go:6:2: error 573549ea-7f0b-47ec-ad30-db25284a3d2a "Log through the shared logger in internal/log. Never print w…": log through internal/log, not fmt.Print
main.go:7:10: error 2de7574e-7106-4986-8ce6-996d06e2703a "Never put credentials in code, config or tests. Read them fr…"
3 rules valid
checked 1 file: 2 errors
```

A non-blocking violation prints `warning` instead of `error`. Lines and columns start at 1; a column counts characters.

| Exit | Meaning |
|---|---|
| 0 | Rules are valid and there is no blocking violation |
| 1 | A rule is invalid, `stele.lock` is out of date, or there is a blocking violation |
| 2 | stele couldn't run: bad flags, no git, unknown `--base` ref, no merge base |

GitHub Actions:

```yaml
name: stele
on: pull_request
permissions:
  contents: read
jobs:
  stele:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
        with:
          fetch-depth: 0 # --base needs the history
      - uses: actions/setup-go@v6
        with:
          go-version: stable
      - run: go install github.com/lictor-ai/stele/cmd/stele@latest
      - run: stele check --base origin/${{ github.base_ref }}
```

Pin a version instead of `@latest` once one is tagged.

## stele.json

Optional, committed next to `stele.md`. These are the defaults:

```json
{ "backstop": true, "block_on_error": false }
```

| Key | Effect |
|---|---|
| `backstop` | `true`: the hook may deny a write to show its rules, and blocks on violations after a write. `false`: no rules are shown before writes; violations after a write are sent as context, never as a block. |
| `block_on_error` | What happens when stele itself fails (the signing key can't be read, `stele.json` invalid, `stele.md` never parsed). `false`: the write goes through and you get a warning. `true`: before a write, the write is blocked and the agent is told why; after a write, the agent is told it was saved but not checked. Only applies when `backstop` is `true`. |

## Limits

1. **If `stele` is missing from `PATH`, or the hook crashes or times out, writes go through.** `block_on_error` only acts when stele runs and fails, so it can't prevent this.
2. **Only Write, Edit and MultiEdit are hooked.** A file written through Bash (`sed -i`, `cat >`) or NotebookEdit gets no rules and no check. `stele check` in CI is the only catch.
3. **The agent can ignore the rules.** It is shown them and told about violations, but it can still choose not to follow them.
4. **A regex check only catches what a pattern can express.** It matches text, not code, so it also fires in comments and strings. RE2 has no lookaround or backreferences. The pattern runs over the whole file: use `(?m)` for `^` and `$` to mean line start and end.
5. **A rule with no `scope`, or only `!` exclusions, is shown for every other file.** Keep such rules few. Rules are chosen by path, not by task.
6. **After an Edit, only the lines it wrote are checked.** If stele can't locate them (Claude Code sent no patch and the new text appears more than once), nothing is checked after that edit. Older violations in the file are left to `stele check`.
7. **Binary files and files over 32 MiB are skipped.** The skip is reported, not silent.
8. **Output is capped.** `stele check` shows at most 100 violations per rule and file; the agent gets at most 20 per message.
9. **A directory with its own `stele.md` or `stele.json` is a separate project.** Its files get only its rules. `stele check` at the parent skips it and says so; run `stele check` in it too. So is a git worktree or submodule (a directory with its own `.git`): it gets no rules from an outer `stele.md`, only from its own.
10. **Nothing leaves the machine.** stele makes no network calls. The rules and violations it hands the agent become part of the conversation, which Claude Code sends to the model as usual.
11. **Unknown keys are accepted silently.** A typo such as `scopes:` leaves the rule unscoped.
12. **The reminder to re-run `stele init` compares tagged releases only.** A build from a commit, or with local changes, stamps `devel` and is never warned about.

## License

See [LICENSE](LICENSE).
