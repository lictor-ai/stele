# stele

stele makes Claude Code follow the rules in your repository's `stele.md`:

- **Before a write**, the agent is shown the rules for the file it is about to write.
- **After a write**, the lines it just wrote are checked against rules that have a regex check.
- **In CI**, `stele check` checks the changed files.

It is one Go binary. It uses no model. It makes no network calls, except `stele publish`, which you run yourself.

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
added stele hooks (PreToolUse, PostToolUse, SessionStart, SubagentStart) to .claude/settings.json
wrote stele.lock
wrote .claude/rules/stele-rules.md
hooks: remind on, check on
```

- It works on the current directory only, and says so if a parent directory looks like the repository root (not when the current directory is a git worktree or submodule).
- It adds `stele hook --installed=<version>` to `.claude/settings.json` on `PreToolUse` (matcher `Write|Edit|MultiEdit|Bash`), `PostToolUse` (matcher `Write|Edit|MultiEdit`), `SessionStart` and `SubagentStart`, each with a timeout of 10 s. The rest of the file is kept. `<version>` is the release tag, or `devel` for a build from a commit (`go install ./cmd/stele` in a clone, or `@latest` before a tag); a build from a commit never replaces a release's stamp.
- It creates `stele.md` only if there is none. stele never writes `stele.md` or `stele.json` after that.
- It compiles the rules: `stele.lock`, and `.claude/rules/stele-rules.md`, which Claude Code loads at the start of every session and subagent, like `CLAUDE.md`. So the agent has the rules before its first write.
- Running it again prints `up to date, nothing written`. Every run ends with the state of the two write hooks.

### Turning the write hooks on and off

The two parts of the hook around a write can each be turned off:

| Flag | On (the default) | Off |
|---|---|---|
| `--remind` | Before a write, a write that needs rules the agent hasn't seen is denied, with the rules. | No write is denied to show rules. The agent still loads `.claude/rules/stele-rules.md` at session start. |
| `--check` | After a write, the lines written are checked, and a blocking violation is sent back as a block. | Nothing is checked after a write. An invalid rule in `stele.md` is still reported. |

```
$ stele init --remind=off
stele init in /home/you/project
updated stele hooks in .claude/settings.json
stele init: kept the existing stele.md
hooks: remind off, check on
```

A switch that is off is written on stele's hooks in `.claude/settings.json` (`stele hook --installed=<version> --remind=off`), so commit it and the team shares it. `stele init` without a flag keeps the switches as they are, so upgrading never turns a hook back on; `--remind=on` turns it on again. Either way, a write to stele's own files still asks you first, and `stele check` in CI is unchanged.

Then write your rules in `stele.md` and run `stele compile`:

```
$ stele compile
3 rules: 0 matched by text, 0 by position, 3 new, 0 dropped
wrote stele.lock, .stele/index.json and .claude/rules/stele-rules.md
```

**Commit** `stele.md`, `stele.lock`, `.claude/rules/stele-rules.md`, `.claude/settings.json`, and `stele.json` if you have one. **Don't commit** `.stele/`; it ignores itself. Don't edit `.claude/rules/stele-rules.md`: `stele compile` writes it from `stele.md`.

If a rule can't be used — a `check` other than `regex`, a pattern that doesn't compile or matches empty text, a `blocking` that isn't `true` or `false` — `stele compile` names it and exits 1. It still writes `stele.lock`, so the rule keeps its id, and the hook enforces the other rules and tells you once a session.

The hook also recompiles when `stele.md` changes, so edits take effect on the next write. Commit the updated `stele.lock` and `.claude/rules/stele-rules.md`. A session keeps the rules file it loaded at its start: a rule changed since is shown again before the next write it applies to.

To remove stele: `stele uninstall`. It removes stele's hooks, `.claude/rules/stele-rules.md` and `.stele/`, and keeps `stele.md`, `stele.lock` and `stele.json`.

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

- **When a session or subagent starts.** Claude Code loads `.claude/rules/stele-rules.md`, every rule with its scope, as it loads `CLAUDE.md`. The agent is also told to create and change files with Write and Edit only, never through Bash: stele sees only those. It is told that reading, searching and running commands through Bash is fine.
- **Before a write**, unless `--remind=off`. A rule the agent loaded from `.claude/rules/stele-rules.md` at the start of its session counts as seen until the session ends, as long as the rule hasn't changed since; this needs Claude Code started in the project's root. The first write in a turn to a file with other rules is denied, with those rules as the reason. The agent reads them and retries. Each rule is shown once per turn, to the main agent and to each subagent; rules that do not fit in one message (about 9,000 bytes) are shown on the next write. A retry of the denied write goes through; parallel writes in the same message, to other files or to the same one, are denied too, once.
- **After a write**, unless `--check=off`. A blocking violation in the lines just written is sent back as a block, with file, line, rule and message. The write is not undone; the agent is told to fix it. Non-blocking violations are added as context.
- **A broken `stele.md`.** The last good rules keep applying, and the agent is told about the error.
- **A write to stele's own files** (`stele.md`, `stele.json`, `stele.lock`, `.claude/rules/stele-rules.md` or anything under `.stele/`, a subproject's too) asks you first, every time, whatever `stele.json` and the switches say. So does a write to Claude Code's settings files, which run stele's hooks: `.claude/settings.json` and `.claude/settings.local.json` anywhere in the project or from the session's directory up to the git root, and your user `settings.json`. A change there can weaken stele or turn it off. Allow it only if you asked for it.

## CI: stele check

| Command | Checks |
|---|---|
| `stele check` | Files changed against `HEAD`: staged, unstaged and untracked |
| `stele check --base <ref>` | Files changed since the merge base of `<ref>` and `HEAD` |
| `stele check --all` | Every tracked file |
| `stele check <path>...` | Exactly these files |

Add `--format json` for JSON. Deleted files are skipped.

`stele check` first checks the rules: `stele.md` must parse, every rule must be valid, and `stele.lock` and `.claude/rules/stele-rules.md` must be committed and current. After upgrading to a stele that writes the rules file, run `stele compile` and commit it, or `stele check` fails. The file is current when it holds the same rules in the same order; a newer stele's wording of its intro alone doesn't make it stale. A file at that path that stele didn't write (no `<!-- Generated by stele` first line) is left alone, and `stele check` fails until you rename it. A rules file over 40,000 characters gets a note: every session loads it whole. In a checkout with no `stele.md`, `stele.json` or `stele.lock` (a submodule, a worktree of a branch without rules, a repository that adds the CI job before its `stele.md`), it says `nothing to check` and exits 0; with `--base` it still reports a deleted `stele.md`. Then it checks the files:

```
$ stele check
main.go:6:2: error 573549ea-7f0b-47ec-ad30-db25284a3d2a "Log through the shared logger in internal/log. Never print w…": log through internal/log, not fmt.Print
main.go:7:10: error 2de7574e-7106-4986-8ce6-996d06e2703a "Never put credentials in code, config or tests. Read them fr…"
3 rules valid
checked 1 file: 2 errors
```

A non-blocking violation prints `warning` instead of `error`. Lines and columns start at 1; a column counts characters.

With `--base`, `stele check` also says when the branch adds, changes or deletes a `stele.md` or `stele.json` (`stele.md: modified on this branch: ...`), so reviewers see a change to the rules. It doesn't change the exit code; in JSON it is `rule_files_changed`.

| Exit | Meaning |
|---|---|
| 0 | Rules are valid (or there are none) and there is no blocking violation |
| 1 | A rule is invalid, `stele.lock` or `.claude/rules/stele-rules.md` is out of date, or there is a blocking violation |
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

Optional, committed next to `stele.md`. This is the default:

```json
{ "block_on_error": false }
```

| Key | Effect |
|---|---|
| `block_on_error` | What happens when stele itself fails (the signing key can't be read, `stele.json` invalid, `stele.md` never parsed). `false`: the write goes through and you get a warning. `true`: before a write, the write is blocked and the agent is told why; after a write, the agent is told it was saved but not checked. It applies only to a part of the hook that is on: with `--remind=off` or `--check=off`, that part's failures only warn. |

`backstop` is no longer read: use `stele init --remind=off`. `stele init` says so when `stele.json` still sets it, and leaves the file as it is:

```
$ stele init
stele init in /home/you/project
stele init: kept the existing stele.md
stele init: stele.json: backstop is no longer read; use stele init --remind=off
up to date, nothing written
hooks: remind on, check on
```

## Publish the rules: stele publish

`stele publish` keeps a copy of the project's `stele.md` in a git repository you choose, as a merge request you review there. Name the repository in `stele.json`:

```json
{ "publish": { "repo": "git@github.com:you/guidelines.git", "path": "project-a/stele.md" } }
```

| Key | Effect |
|---|---|
| `repo` | The repository: `https://host/path`, `ssh://host/path` or `user@host:path`. No local path, no other transport, no password in the URL. In the `user@host:path` form the path can't start with `/`: write `ssh://user@host/abs/path` instead. |
| `path` | Where `stele.md` goes in it. Default `stele.md`. Letters, digits, `.`, `_`, `-` and `/`, ending in `.md`. |

One repository per project needs only `repo`. Several projects can share one repository, each with its own `path`.

What it does:

- **It sends the committed `stele.md`**, as it is in the project's last commit. A `stele.md` with changes not yet committed is refused. It needs the project id in `stele.lock`: run `stele compile` first.
- **It commits it on the repository's default branch** and pushes that commit to the branch `stele/publish/<path>`. Running it again replaces that branch when `stele.md` changed or the default branch moved on; it never overwrites a commit on it that it did not make for this project.
- **It opens a merge request** into the default branch, or finds the one already open, on github.com and gitlab.com. It never merges. Run again with nothing new, it reopens a request that was closed.
- **It never asks for a token.** It uses a login you already have, in this order: the `gh` or `glab` login, git's stored credential (Git Credential Manager), then `GITHUB_TOKEN` or `GITLAB_TOKEN`. When none works, it prints the host's page to open the request yourself and tries to open it in the browser.
- **Nothing changes when the default branch already has this `stele.md`.**

Its working copy of the repository is a bare clone under your user cache directory (`stele/publish/` in it), never in the project. It exits 0 when the branch is pushed, even if the merge request could not be opened, and 1 when nothing was pushed:

```
$ stele publish
stele publish: stele.md has changes that are not committed; commit it first
```

```
$ stele publish
stele publish: bad publish setting in stele.json: repo "file:///srv/rules.git": only https and ssh are allowed; use https://host/path, ssh://host/path or user@host:path
```

What leaves the machine: the committed `stele.md`, its `path`, the project's commit hash and id, and your git name and email, to `repo`; and, to github.com or gitlab.com only, the login it found.

## Test results

[test/functional/](test/functional/) holds two kinds of tests:

- **Black-box tests** of the binary: 52 tests of init, compile, check and the hook, built and run with `go test ./test/functional/`.
- **An agent eval** that gives headless Claude Code the same coding tasks in three setups by default and grades every run the same way:
  - no rules;
  - the rules pasted into `CLAUDE.md`;
  - stele.

Every experiment, with its numbers, is in [test/functional/agent/results/](test/functional/agent/results/README.md). The main results, with Claude Sonnet 5.5:

| | No rules | Rules in `CLAUDE.md` | stele v1.0.0-dev.2 |
|---|---|---|---|
| 9 tasks, 18 runs per setup: runs that broke no blocking rule | 33% | 100% | 100% |
| tokens per run | 143k | 163k | 163k |
| A task that pulls against a rule, 8 runs: runs that broke no blocking rule | | 5 of 8 | 8 of 8 |

- **The 9 tasks** range from adding one function to building a small HTTP service. The large ones run under a 74-rule `stele.md`.
- **The task that pulls against a rule** asks for a test of an HTTP client. The rules forbid a real test server. With the rules in `CLAUDE.md`, the agent used one anyway in 3 of 8 runs. stele flagged each use right after the write, and the agent fixed it.
- **The hook adds about 0.1 s per write** on Windows: Claude Code runs it before and after every write. With 500 rules it adds up to 0.17 s. The measured times are in [test/functional/results/](test/functional/results/), one file per stele measured.

## Limits

1. **If `stele` is missing from `PATH`, or the hook crashes or times out, writes go through.** `block_on_error` only acts when stele runs and fails, so it can't prevent this.
2. **Only Write, Edit and MultiEdit are checked.** The agent is told at the start of each session to write files with those only. A Bash command that writes, by redirection, `tee`, `sed -i` or `perl -i`, into a file a scoped rule covers is stopped once per file each turn: the agent gets the rules and is told to use Write or Edit; run again, the command goes through unchecked. With `--remind=off` it is not stopped at all. stele reads the command, it does not run it, so scripts, generators, `xargs`, `find -exec`, `$VAR` targets, brace expansion and writes into files only unscoped rules cover are not seen. NotebookEdit and PowerShell are not hooked. `stele check` in CI is the only catch.
3. **The agent can ignore the rules.** It is shown them and told about violations, but it can still choose not to follow them.
4. **A regex check only catches what a pattern can express.** It matches text, not code, so it also fires in comments and strings. RE2 has no lookaround or backreferences. The pattern runs over the whole file: use `(?m)` for `^` and `$` to mean line start and end.
5. **A rule with no `scope`, or only `!` exclusions, is shown for every other file.** Keep such rules few. Rules are chosen by path, not by task.
6. **After an Edit, only the lines it wrote are checked.** If stele can't locate them (Claude Code sent no patch and the new text appears more than once), nothing is checked after that edit. Older violations in the file are left to `stele check`.
7. **Binary files and files over 32 MiB are skipped.** The skip is reported, not silent.
8. **Output is capped.** `stele check` shows at most 100 violations per rule and file; the agent gets at most 20 per message.
9. **A directory with its own `stele.md` or `stele.json` is a separate project.** Its files get only its rules. `stele check` at the parent skips it and says so; run `stele check` in it too. So is a git worktree or submodule (a directory with its own `.git`): it gets no rules from an outer `stele.md`, only from its own. `stele check` there says nothing to check until it has its own `stele.md`, `stele.json` or `stele.lock`.
10. **Only `stele publish` sends anything, and only when you run it.** It sends the committed `stele.md`, its path, the project's commit hash and id, and your git name and email to the `repo` in `stele.json`, and a login to github.com or gitlab.com. stele makes no other network call. The rules and violations it hands the agent become part of the conversation, which Claude Code sends to the model as usual.
11. **Unknown keys are accepted silently.** A typo such as `scopes:` leaves the rule unscoped.
12. **The reminder to re-run `stele init` compares tagged releases only.** A build from a commit, or with local changes, stamps `devel` and is never warned about. **A stele from before `--remind` and `--check` must not run `stele init` on hooks that carry a switch: upgrade it first.** It doesn't recognise the switches, so its `stele init` adds a second set of hooks, and it runs both hooks whatever the switches say. Every release so far, v1.0.0-dev.2 included, still tells you to run `stele init` when it meets hooks a newer release wrote. Only a stele with the switches tells you to upgrade instead.
13. **Asking before a write to stele's own files covers Write, Edit and MultiEdit, and their names and links.** A hard link to one of them, or an 8.3 short name, is caught only for the root's `stele.md`, `stele.json`, `stele.lock`, `.stele/` and `.claude` settings files. A Bash command that visibly writes, removes, moves or copies onto one of them is asked about too. Not asked about: other Bash forms, a NotebookEdit, managed settings, settings passed with `--settings` or the environment, and a session started above the stele project with hooks registered there by hand. With no one to answer, as in `claude -p`, Claude Code refuses these writes, even ones you asked for.
14. **With `--check=off`, only `stele check` finds violations.** With both `--remind=off` and `--check=off`, the agent still loads the rules at session start, but nothing enforces them until CI.
15. **`stele publish` opens merge requests on github.com and gitlab.com only.** Any other host, a self-hosted GitHub or GitLab included, gets the branch pushed and is told to open the request by hand, with no link.
16. **`stele publish` needs a repository with at least one commit, and git access to it.** git and Git Credential Manager are never allowed to prompt. ssh still can: a key's passphrase or an unknown host key is asked by ssh itself.
17. **`stele publish` refuses a `stele/publish/<path>` branch holding commits it did not make for this project.** Two projects that publish to the same `path` of one repository get that error; give each its own `path`.

## License

See [LICENSE](LICENSE).
