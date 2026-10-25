# stele publish

Status: proposed, not built. Date: 2026-10-25.

## Why

A developer writes a project's rules in its `stele.md`. They want those rules
versioned in a git repository of their choosing, outside the project: one
repository for all their projects, or one per project. A later version reads
rules back from there (#31). This one only stores them.

`stele publish` sends the project's `stele.md` to that repository as a merge
request. The developer reviews and merges it there.

Not in this version: one project reusing another's rules, extending them, or
excluding some of them. Reading rules from the repository stays #31.

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
| `repo` | Any git URL the developer can push to, SSH or HTTPS. Required. |
| `path` | Where `stele.md` goes inside that repository. Default `stele.md`. |

One repository per project is `{"repo": "..."}`. One repository for several
projects gives each project its own `path`. stele imposes no layout.

Without a `publish` key, `stele publish` fails with a message that shows the
key to add.

## What `stele publish` does

1. **Checks the project.** `stele.md` must exist and must match the version
   committed at `HEAD`. A `stele.md` with uncommitted changes is refused, so the
   published file is always one the project's history has.
2. **Updates its clone** of `repo` under stele's cache directory
   (`os.UserCacheDir()/stele/publish/<hash of the URL>`), never inside the
   project. It runs `git` from `PATH`.
3. **Branches** from the repository's default branch, as `stele/<project>`.
   `<project>` is the name of the project's root folder.
4. **Copies** `stele.md` to `path`. If the file there is already identical, it
   says so and stops: no branch, no push, no merge request.
5. **Commits** with the developer's own git identity, with the message
   `stele: publish rules from <project>`, and the project's commit hash in the
   body.
6. **Pushes** the branch with the developer's own git credentials. It force-
   pushes, because `stele/<project>` belongs to stele: publishing again
   replaces the earlier proposal.
7. **Opens a merge request** from that branch into the default branch, or finds
   the one already open for it, and prints its link.

The developer merges it on the host. stele does not merge.

## Opening the merge request

stele calls the host's API from its own code. There is nothing extra to
install. It recognises the host from the URL: `github.com` is GitHub, a host
named `gitlab.com` or containing `gitlab` is GitLab.

**stele never asks for a token.** It reuses a login the developer already has,
in this order:

1. The host's CLI login, if that CLI is installed and logged in:
   `gh auth token --hostname <host>`, or the token in `glab`'s login for the
   host. Check which `glab` command prints it before building.
2. git's stored credential for the host, from `git credential fill`. Git
   Credential Manager, the default on Windows, stores one there.
3. `GITHUB_TOKEN` or `GITLAB_TOKEN`, if set.

`gh` and `glab` are run only from `PATH`, and never from inside the project
(D43).

**No token, or another host.** The branch is already pushed. stele prints the
host's page for a new merge request, prefilled with the branch, and opens it in
the browser when one is available:

- GitHub: `https://<host>/<owner>/<repo>/compare/<base>...stele/<project>?expand=1`
- GitLab: `https://<host>/<group>/<repo>/-/merge_requests/new?merge_request[source_branch]=stele/<project>`
- Any other host: the link the host printed when the branch was pushed, if
  any, otherwise the branch name.

This follows git-spice, which opens merge requests on several hosts from a Go
binary and reuses existing logins, and Git Town, which opens the prefilled
page in the browser.

## Errors

Each one names what failed and what to do. None leaves a change in the project.

| Case | Result |
| :-- | :-- |
| No `publish` key | Exit 1. Shows the key to add. |
| No `stele.md` | Exit 1. Nothing to publish. |
| `stele.md` has uncommitted changes | Exit 1. Commit it first. |
| `git` not on `PATH` | Exit 1. |
| Clone, fetch or push fails | Exit 1. Shows git's own message, which says whether access was denied. |
| The API call fails | Exit 0. The branch is pushed; stele prints the prefilled page, as with no token. |

## What leaves the machine

D53 says data leaves only through a sink, from `stele push` or
`stele check --push`. `stele publish` is a second way out, and changes D53:
it sends `stele.md`, and only to the `repo` the developer wrote in `stele.json`,
and only when they run it. It sends nothing else: no code, no log, no index.

It publishes `stele.md`, not the compiled index. The index is signed for one
machine (#27) and can be rebuilt from `stele.md` anywhere.

## Testing

- Unit: reading the `publish` key and its default; host detection from SSH and
  HTTPS URLs; the prefilled links; the token lookup order, with `gh`, `glab`,
  git and the environment faked.
- Integration, with no network: a bare git repository on disk as `repo`.
  Publish, then publish again unchanged (stops), then after an edit (the branch
  is replaced). Uncommitted `stele.md` is refused.
- The GitHub and GitLab API calls against a local HTTP test server.
- One manual run against a real GitHub repository and a real GitLab one before
  release.

## Decisions

| # | Decision |
| :-- | :-- |
| D64 | `stele publish` sends the committed `stele.md` to the `repo` and `path` named under `publish` in `stele.json`, as a merge request on branch `stele/<project>`. |
| D65 | It reuses an existing login (`gh`/`glab`, git credentials, environment) and never asks for a token. Without one, it opens the host's prefilled page. |
| D66 | D53 gains a second exit: `stele publish`, run by the developer, sends `stele.md` and nothing else. |
