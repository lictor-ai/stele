# stele publish

Status: proposed, not built. Date: 2026-10-25. Part of v1, by ADR-0007
(D64–D66).

## Why

A developer writes a project's rules in its `stele.md`. They want those rules
versioned in a git repository of their choosing, outside the project: one
repository for all their projects, or one per project. A later version reads
rules back from there (#31). This one only stores them.

`stele publish` sends the project's `stele.md` to that repository as a merge
request. The developer reviews and merges it there.

Not in this version: one project reusing another's rules, extending them, or
excluding some of them; self-hosted GitHub or GitLab (they get the prefilled
page, below); reading rules from the repository (#31).

## The setting

`stele.json` gets one key. The developer writes it; stele never does (D38).

```json
{
  "publish": {
    "repo": "git@github.com:me/guidelines.git",
    "path": "project-a/stele.md"
  }
}
```

| Key | Meaning |
| :-- | :-- |
| `repo` | The repository to publish to. Required. |
| `path` | Where `stele.md` goes inside it. Default `stele.md`. |

One repository per project is `{"repo": "..."}`. One repository for several
projects gives each project its own `path`. stele imposes no layout.

`stele.json` is committed, so a cloned repository chooses these values. Both
are checked before anything runs, and a bad value is an error that names it:

- **`repo`** is `https://host/...`, `ssh://host/...`, or `user@host:path`. It
  never starts with `-`. Local paths, `file://`, `ext::` and every other
  transport are refused.
- **`path`** is relative, uses `/`, and has no empty, `.` or `..` part. No part
  is `.git` in any case, ends with `.` or a space, or contains `\`, `:` or a
  control character. It ends in `.md`.

Without a `publish` key, `stele publish` fails with a message that shows the
key to add.

## What `stele publish` does

1. **Reads the rules from git.** The project must be in a git repository, and
   `stele.md` must be tracked and unchanged since `HEAD` (`git diff --quiet
   HEAD -- stele.md`). stele publishes the committed blob, `git show
   HEAD:<path of stele.md from the git top>`, so line endings and an
   uncommitted edit cannot change what is sent. A project without git, an
   untracked `stele.md` and an edited one each get their own error. This is the
   one place outside a source plugin that reads a rule file (an exception to
   D40): it copies the file and never parses it.
2. **Updates its clone** of `repo`: a bare repository under stele's cache
   directory (`os.UserCacheDir()/stele/publish/<SHA-256 of the URL>`), never
   inside the project. A file lock keeps two publishes out of it at once.
   Nothing is ever checked out, so no file of the published repository is
   written to disk as a file and none of its hooks runs.
3. **Finds the base**, the repository's default branch (its `HEAD`). An empty
   repository has none: stele stops and says to push a first commit.
4. **Stops when nothing changed**: when the blob at `path` on the default
   branch, or on the publish branch if it exists, is already the one from step
   1. Nothing is pushed.
5. **Commits** on top of the default branch, with git's plumbing
   (`hash-object`, `update-index` on a temporary index, `write-tree`,
   `commit-tree`), using the developer's git identity. The message is
   `stele: publish <path>`, with `Project-Commit: <hash of the project's HEAD>`
   in the body.
6. **Pushes** to the publish branch `stele/publish/<path>`, for example
   `stele/publish/project-a/stele.md`. The branch follows from `path`, which is
   unique in the repository, so two projects never share one, and every clone
   or worktree of a project uses the same one. stele checks the name with
   `git check-ref-format`. The push replaces an earlier publish
   (`--force-with-lease`, against the tip it just fetched). It refuses if that
   tip is a commit stele did not make (one whose message is not
   `stele: publish <path>`), so a person's commits on the branch are never
   overwritten.
7. **Opens a merge request** from that branch into the default branch, or finds
   the one already open for it, and prints its link.

The developer merges it on the host. stele does not merge.

### Running git safely

Every git call:

- passes `--` before a URL or path, and names refs in full (`refs/heads/...`);
- runs with `-c protocol.allow=never -c protocol.https.allow=always -c
  protocol.ssh.allow=always`, `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never`
  and stdin closed, so it can neither run a transport command nor prompt;
- never fetches submodules.

`git`, `gh`, `glab` and the browser opener are found on `PATH` and resolved to
an absolute path. A program whose path is inside the project is refused, as
D43 refuses plugins from inside the repository.

## Opening the merge request

stele calls the GitHub or GitLab API itself. Nothing extra is installed.

**Only two hosts receive a token: `github.com` (API `api.github.com`) and
`gitlab.com`, over HTTPS.** The host is the one in `repo`, matched exactly. Any
other host, including a self-hosted GitHub or GitLab, gets no token and no API
call: it gets the prefilled page.

**stele never asks for a token.** It reuses a login the developer already has,
in this order, and stops at the first it finds:

1. The host's CLI login, if that CLI is installed and logged in to that host:
   `gh auth token --hostname github.com`, or `glab`'s token for `gitlab.com`.
   Check which `glab` command prints it before building.
2. git's stored credential for that host, from `git credential fill`, run with
   prompts disabled as above. Git Credential Manager, the default on Windows,
   stores an OAuth token there. If the host rejects it (for example a stored
   password), stele falls back to the prefilled page.
3. `GITHUB_TOKEN` for `github.com` only, `GITLAB_TOKEN` for `gitlab.com` only.

The token is held in memory for the call and never written or printed.

**The prefilled page.** With no token, a rejected one, an API error, or
another host, the branch is already pushed. stele prints a page to open the
merge request there:

- GitHub: `https://<host>/<owner>/<repo>/compare/<base>...<branch>?expand=1`
- GitLab: `https://<host>/<path>/-/merge_requests/new?merge_request%5Bsource_branch%5D=<branch>`
- Any other host: no link. stele prints the repository and the branch name.

stele builds every link itself, over HTTPS, on the host of `repo`, with each
part percent-encoded. It never prints or opens a link the remote sent in its
push output. A link from the API (`html_url`, `web_url`) is used only if it is
HTTPS on the same host. Control characters are removed before printing.

stele always prints the link. It also tries to open it: `rundll32
url.dll,FileProtocolHandler` on Windows, `open` on macOS, `xdg-open` on Linux,
with the link as a single argument and never through a shell. A failure to
open is ignored.

This follows git-spice, which opens merge requests on several hosts from a Go
binary and reuses existing logins, and Git Town, which opens the prefilled
page in the browser.

## Errors

Each one names what failed and what to do. None changes the project.

| Case | Result |
| :-- | :-- |
| No `publish` key, or a bad `repo` or `path` | Exit 1. Shows what to write. |
| No git, `stele.md` untracked, or edited since `HEAD` | Exit 1, each with its own message. |
| `git` not on `PATH`, or found inside the project | Exit 1. |
| Empty repository | Exit 1. Push a first commit. |
| Clone, fetch or push fails | Exit 1. Shows git's message, which says whether access was denied. |
| The publish branch has commits stele did not make | Exit 1. Names the branch. |
| No token, rejected token, API error | Exit 0. The branch is pushed; stele prints the prefilled page. |

## What leaves the machine

D53 says data leaves only through a sink, from `stele push` or
`stele check --push`. ADR-0007 adds `stele publish` as a second way out, run
only by the developer, only to the `repo` in `stele.json`. It sends:

- the committed `stele.md`;
- `path`, in the branch name and the commit message;
- the hash of the project's `HEAD` commit;
- the developer's git name and email, as the commit's author;
- to `github.com` or `gitlab.com` only, the token, to open the merge request.

Nothing else: no code, no log, no index. It publishes `stele.md`, not the
compiled index, which is signed for one machine (#27) and can be rebuilt from
`stele.md` anywhere.

## Where the code goes

- `internal/publish` holds the work and returns values and errors; it never
  prints or exits. It is the only package that imports `net/http` and the only
  one besides `stele check` that starts processes.
- `cmd/stele/publish.go` parses flags, prints and picks the exit code.
- `stele hook` never reaches either.

The change that builds it updates `stele.md` with it: the network rule allows
`net/http` in `internal/publish` only, and the `os/exec` rule names it. It also
adds `stele publish` to the README, with its limits: GitHub and GitLab only for
the merge request, no self-hosted instances, no empty repository.

## Testing

- Unit: reading `publish`, its default and every refused `repo` and `path`
  above; host matching (`gitlab.attacker.example` gets no token); the
  prefilled links and their encoding; the token order, with `gh`, `glab`, git
  and the environment faked; a tool on `PATH` inside the project is refused.
- Integration, with no network: a bare repository on disk as the remote,
  reached through a test-only hook that is not a `repo` value. Publish, then
  again unchanged (stops), then after an edit (the branch is replaced).
  Untracked, edited and CRLF `stele.md`; an empty remote; a publish branch with
  someone else's commit.
- The GitHub and GitLab API calls against a local HTTP test server.
- One manual run against a real GitHub repository and a real GitLab one before
  release.

## Decisions

| # | Decision |
| :-- | :-- |
| D64 | `stele publish` sends the committed `stele.md` to the `repo` and `path` under `publish` in `stele.json`, as a merge request from branch `stele/publish/<path>`. It writes nothing to disk from the published repository and never overwrites commits it did not make. |
| D65 | It reuses an existing login (`gh`/`glab`, git's stored credential, the environment) and never asks for a token. Tokens go only to `github.com` and `gitlab.com`; every other case gets a prefilled page that stele builds itself. |
| D66 | D53 gains a second exit: `stele publish`, run by the developer, sends `stele.md`, its `path`, the project's commit hash and the author's identity, and nothing else. Network calls are allowed in `internal/publish` only (ADR-0007). |
