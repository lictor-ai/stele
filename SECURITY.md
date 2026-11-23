# Security

## Reporting a vulnerability

Report it privately, through GitHub: on the repository's Security tab, choose
**Report a vulnerability**, or go straight to
<https://github.com/lictor-ai/stele/security/advisories/new>. Only the
maintainers see the report. Please don't open a public issue for it.

Include:

- the stele version (the first line of `stele status`) and your OS;
- the agent (Claude Code or Codex) and its version, if it is involved;
- the smallest `stele.md`, `stele.json` and commands or tool calls that show it;
- what happened, and what you expected.

## Supported versions

Only the latest release gets fixes. Today that is the 1.6 line. A fix ships
in a new release; older releases are not patched.

## What counts

A vulnerability is a way to defeat what stele promises. For example:

- the hook lets a write through without showing the rules it should show, or
  without checking it, when its switches are on;
- stele runs code that a repository brings with it: a program on a path inside
  the project, a plugin not listed in `stele.json` or found inside the
  repository, a git or scanner it shouldn't start;
- stele sends anything off the machine other than what the README says
  (`stele publish`, a sink you listed, a fetch of shared rules);
- a signed file under `.stele/` (the index, session state, the log) can be
  forged or tampered with without stele noticing, beyond what README's Limits
  say.

Some gaps are known and documented. README's [Limits](README.md#limits) lists
them: for example, a write through a shell command stele can't read, or a
program running as you that reads the signing key. Those are not
vulnerabilities, but a way around one that the Limits don't describe is.

## Fixes

A fix ships in a release, with a published GitHub security advisory that names
the affected and fixed versions. Past advisories are on the
[Security tab](https://github.com/lictor-ai/stele/security/advisories).
