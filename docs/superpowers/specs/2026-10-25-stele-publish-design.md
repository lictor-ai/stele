# stele publish

Status: built (#79). Run by hand against a real GitHub repository on
2026-10-27 (first publish, unchanged, reopened, edited, merged, stale branch,
a person's commit on the branch); the GitLab run is still to do. Date:
2026-10-25. Part of v1, by ADR-0007
(D64–D66).

## Why

A developer writes a project's rules in its `stele.md`. They want those rules
versioned in a git repository of their choosing, outside the project: one
repository for all their projects, or one per project. A later version reads
rules back from there (#31). This one only stores them.

`stele publish` sends the project's `stele.md` to that repository as a merge
request. The developer reviews and merges it there.

Not in this version: one project reusing another's rules, extending them, or
excluding some of them; self-hosted GitHub or GitLab (stele cannot tell them
from any other host, so they are told to open the request by hand, below);
reading rules from the repository (#31).

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
  never starts with `-` and holds no space or control character. Local paths,
  `file://`, `ext::` and every other transport are refused, and so is a
  password in it (`https://user:secret@...`, `user:secret@host:path`): stele
  prints `repo` in messages, and git's own login supplies the password. The
  error never shows the password.
- **`path`** is relative, uses `/`, ends in `.md`, and is at most 200 bytes.
  Each part is made of letters, digits, `.`, `_` and `-` only, does not start
  with `.` or `-`, holds no `..` anywhere (`x..md` included, which
  `check-ref-format` refuses), and does not end with `.` or `.lock`. So there
  is no `.git`, no `\` or `:`, and every `path` makes a valid branch name.

Without a `publish` key, `stele publish` fails with a message that shows the
key to add.

## What `stele publish` does

1. **Reads the rules from git.** The project must be in a git repository, and
   `stele.md` must be tracked and unchanged since `HEAD` (`git diff --quiet
   HEAD -- stele.md`), and committed as a plain file, not a symlink. stele
   publishes the committed blob, `git cat-file blob HEAD:<path of stele.md
   from the git top>` (`cat-file`, not `show`, which can apply textconv), at
   most 16 MiB, so line endings and an
   uncommitted edit cannot change what is sent. A project without git, an
   untracked `stele.md` and an edited one each get their own error. git must
   have found the checkout's own repository, `<top>/.git` or the directory a
   worktree's `.git` file names: a project laid out as a bare repository,
   whose config could run a filter, is refused before git reads a file
   through it. This is the
   one place outside a source plugin that reads a rule file (an exception to
   D40, ADR-0007): it copies the file and never parses it.
   It also reads the project id from `stele.lock` (D51); without one, it says
   to run `stele compile` first.
2. **Updates its clone** of `repo`: a bare repository under stele's cache
   directory (`os.UserCacheDir()/stele/publish/<SHA-256 of the URL>`), never
   inside the project. The first clone takes only the default branch
   (`--single-branch`, no tags); the publish branch comes with the fetch. A
   file lock keeps two publishes out of it at once.
   Nothing is ever checked out, so no file of the published repository is
   written to disk as a file and none of its hooks runs.
3. **Finds the base**, the repository's default branch (its `HEAD`, from
   `git ls-remote --symref`, run before the clone). An empty repository has
   none: stele stops and says to push a first commit, and leaves no cache.
4. **Stops when nothing changed.** If the blob at `path` on the default
   branch is already the one from step 1, it says so and stops. If the publish
   branch already has it, on top of the current default branch, it skips
   steps 5 and 6 and goes to step 7, so a merge request that failed to open,
   or was closed, is opened again. A publish branch on an older base is
   rebuilt: its merge request would otherwise bring that base back.
5. **Commits** on top of the default branch, with git's plumbing and a
   temporary index: `read-tree <base>`, `hash-object -w --no-filters --stdin`
   (the blob on stdin), `update-index
   --cacheinfo` for `path`, `write-tree`, `commit-tree`. Every other file of
   the base stays as it is. The author and committer are the developer's
   identity as the project sees it (`git var GIT_AUTHOR_IDENT` run in the
   project), passed as `GIT_AUTHOR_*` and `GIT_COMMITTER_*`, because the bare
   clone reads only the global config. The message is `stele: publish <path>`,
   with two trailers: `Stele-Project: <project id>` and
   `Project-Commit: <hash of the project's HEAD>`.
6. **Pushes** to the publish branch `stele/publish/<path>`, for example
   `stele/publish/project-a/stele.md`. Every clone or worktree of a project
   uses the same one. stele checks the name with `git check-ref-format`. The
   push replaces an earlier publish (`--force-with-lease`, against the tip it
   just fetched). It refuses if that tip is not a stele commit for this
   project (a subject of exactly `stele: publish <path>`, alone in its
   paragraph, and the same `Stele-Project` in the last paragraph), so a
   person's commits are never overwritten, and two projects that chose the
   same `path` get an error instead of replacing each other. On success,
   git's push output is discarded.
7. **Opens a merge request** from that branch into the default branch, or finds
   the one already open for it, and prints its link. A closed request is
   reopened only when step 6 pushed nothing: GitHub cannot reopen a pull
   request whose branch was force-pushed after it closed. If reopening fails,
   a new one is opened.

The developer merges it on the host. stele does not merge.

### Running git safely

Every git call:

- passes `--` before a URL or path, and names refs in full (`refs/heads/...`).
  `check-ref-format` takes no `--`; the name it checks starts with `refs/`;
- runs with `-c safe.bareRepository=explicit`, so git uses a bare repository
  only when stele names it with `--git-dir`;
- runs with `-c protocol.allow=never -c protocol.https.allow=always -c
  protocol.ssh.allow=always -c credential.interactive=never`,
  `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never` and an empty `GIT_ASKPASS`
  (which makes git skip `core.askPass` and `SSH_ASKPASS` too), so it can
  neither run a transport command nor prompt;
- gets no stdin, except the two calls that read it: `git credential fill` its
  request and `hash-object --stdin` the blob, each a fixed buffer, never the
  terminal;
- runs with `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and the other
  variables that would point it elsewhere removed, and on the bare clone with
  `--git-dir`, so git never looks for a repository above it;
- never fetches or pushes submodules;
- has at most 30 minutes when it talks to the remote (`ls-remote`, `clone`,
  `fetch`, `push`) and 5 otherwise;
- has at most 1 MiB of its output and of its error kept (16 MiB for
  `stele.md` itself); the rest is read and dropped.

ssh's own prompts stay possible: a key's passphrase, or an unknown host key.
They come from the developer's own ssh setup, which stele does not override.

When git fails, stele shows its message with control characters removed and
without the `remote:` lines that hold a link: stele never shows a link the
remote chose. A refusal such as `remote: Permission denied` stays.

`git`, `gh`, `glab` and the browser opener are found on `PATH` and resolved to
an absolute path. A program whose path is inside the project, or inside the
git repository that holds it (the nearest directory above with a `.git`,
found without running git), is refused, as D43 refuses plugins from inside
the repository.

## Opening the merge request

stele calls the GitHub or GitLab API itself. Nothing extra is installed.

**Only two hosts receive a token: `github.com` (API `api.github.com`) and
`gitlab.com`, over HTTPS.** The host is the one in `repo`, matched exactly. Any
other host, including a self-hosted GitHub or GitLab, gets no token and no API
call: stele prints the repository and the branch to open the request from.
The API endpoints are fixed; nothing in `repo`, its port included, changes
where a token goes. The HTTP client follows no redirect.

**stele never asks for a token.** It reuses a login the developer already has,
trying these in order. When the host rejects one, it tries the next:

1. The host's CLI login, if that CLI is installed and logged in to that host:
   `gh auth token --hostname github.com`, or `glab config get token --host
   gitlab.com`. Both print the bare token on stdout. They run outside the
   project, with nothing on stdin and with `GH_TOKEN`, `GITHUB_TOKEN`,
   `GITLAB_TOKEN` and the other token variables removed, since both CLIs
   would otherwise answer with those first.
2. git's stored credential for that host, from `git credential fill`, run with
   prompts disabled as above. Git Credential Manager, the default on Windows,
   stores an OAuth token there. A stored password is rejected by the host, and
   stele moves on.
3. `GITHUB_TOKEN` for `github.com` only, `GITLAB_TOKEN` for `gitlab.com` only.

The token is held in memory for the call and never written or printed. A
login is rejected, and the next one tried, on a 401 or a 403, and on a 404
to the first call: a private repository is invisible to a login without
access to it.

Both hosts then behave the same. An open request from the branch is
"found". When nothing was pushed, a closed one is reopened; if that fails, a
new one is opened, and if that fails too the error gives both reasons. When
GitHub answers 422 to the new one ("a pull request already exists", into
another base say), or GitLab 409, the open one from the branch is the answer.

**The prefilled page.** With no token, every one rejected, or an API error on
github.com or gitlab.com, the branch is already pushed. stele prints a page to
open the merge request there:

- GitHub: `https://<host>/<owner>/<repo>/compare/<base>...<branch>?expand=1`
- GitLab: `https://<host>/<path>/-/merge_requests/new?merge_request%5Bsource_branch%5D=<branch>`
- Any other host: no link. stele prints the repository and the branch name.

stele builds every link itself, over HTTPS, on the host of `repo`, with each
part percent-encoded. It never prints or opens a link the remote sent in its
push output. A link from the API (`html_url`, `web_url`) is used only if it is
HTTPS on the same host. Before printing, escape sequences, control characters
and the characters that reorder text are removed, and every field is one
line: a newline or tab in it becomes a space. Only git's message, when it
fails, keeps its lines, each cleaned.

stele always prints the link, the merge request's or the prefilled page. It
also tries to open it: `rundll32
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
| No project id in `stele.lock` | Exit 1. Run `stele compile`. |
| `git` not on `PATH`, or found inside the project | Exit 1. |
| Empty repository | Exit 1. Push a first commit. |
| Clone, fetch or push fails | Exit 1. Shows git's message, which says whether access was denied. |
| The publish branch has commits stele did not make for this project | Exit 1. Names the branch. |
| Another `stele publish` is using the same cache | Exit 1. Try again when it ends. |
| No token, rejected token, API error | Exit 0. The branch is pushed; stele prints the prefilled page. |
| An argument on the command line | Exit 2, as for every command. |

## What leaves the machine

D53 says data leaves only through a sink, from `stele push` or
`stele check --push`. ADR-0007 adds `stele publish` as a second way out, run
only by the developer, only to the `repo` in `stele.json`. It sends:

- the committed `stele.md`;
- `path`, in the branch name and the commit message;
- the hash of the project's `HEAD` commit and the project id;
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
- `stele hook` never reaches either: `cmd/stele/hookreach_test.go` follows the
  hook's imports, and a `stele.md` rule refuses the import on the hook's path.

The change that builds it updates `stele.md` with it: the network rule allows
`net` and `net/...` in `internal/publish` only, the `os/exec` rule names it, and the D40
rule names its one read of `stele.md`. It also
adds `stele publish` to the README, with its limits: GitHub and GitLab only for
the merge request, no self-hosted instances, no empty repository.

## Testing

- Unit: reading `publish`, its default and every refused `repo` and `path`
  above; host matching (`gitlab.attacker.example` gets no token); the
  prefilled links and their encoding; the token order, with `gh`, `glab`, git
  and the environment faked; a tool on `PATH` inside the project is refused.
- Integration, with no network: a bare repository on disk as the remote,
  reached through a package variable that only tests set. It is not a flag,
  an environment variable or a `repo` value, and it is the only thing that
  adds `-c protocol.file.allow=always`. Publish, then
  again unchanged (stops), then after an edit (the branch is replaced).
  Untracked, edited and CRLF `stele.md`; an empty remote; a publish branch with
  someone else's commit, or another project's; a merge request closed and
  published again unchanged (reopened); every other file of the base kept.
- The GitHub and GitLab API calls against a local HTTP test server.
- One manual run against a real GitHub repository and a real GitLab one before
  release.

## Decisions

| # | Decision |
| :-- | :-- |
| D64 | `stele publish` sends the committed `stele.md` to the `repo` and `path` under `publish` in `stele.json`, as a merge request from branch `stele/publish/<path>`. It writes nothing to disk from the published repository and never overwrites commits it did not make for this project. It reads `stele.md` from git to copy it, never parsing it: the one exception to D40. |
| D65 | It reuses an existing login (`gh`/`glab`, git's stored credential, the environment) and never asks for a token. Tokens go only to `github.com` and `gitlab.com`. When no merge request opens there, stele prints a prefilled page it builds itself; any other host is told the branch to open one from. |
| D66 | D53 gains a second exit: `stele publish`, run by the developer, sends `stele.md`, its `path`, the project's commit hash and id, and the author's identity, and nothing else. Network calls are allowed in `internal/publish` only (ADR-0007). |
