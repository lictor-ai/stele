# stele

Guideline enforcement for coding agents. Go, single binary.

## Which documents are authoritative

1. `docs/superpowers/specs/2026-08-29-stele-v1-design.md` — the v1 spec. It wins
   where documents disagree.
2. `docs/adr/0002-engine-and-extension-points.md` — the extension points
   (sources, agent adapters, checkers) and the plugin protocol. The spec carries
   its decisions as D41–D48.
3. `docs/adr/0003-ready-for-a-dashboard.md` — formats and ids a later org-wide
   dashboard needs: versions on every format, a project id, source-supplied rule
   ids, sinks, `stele status`. The spec carries its decisions as D49–D55.
4. `docs/adr/0004-v1-for-one-developer.md` — v1's scope: one developer, rules
   chosen by `scope`, delivered and checked by Claude Code hooks; no MCP server
   or matcher in v1. Where it and the spec disagree about v1, it wins. The spec
   carries it as D58.
5. `docs/adr/0005-rules-loaded-at-session-start.md` — the rules reach the agent
   through a generated `.claude/rules/stele-rules.md` that Claude Code loads at
   launch, and the deny shows only rules a session has not loaded. The spec
   carries it as D62.
6. `docs/adr/0006-switching-the-write-hooks.md` — `stele init` switches the
   pre-write deny and the post-write check on and off, as flags on the hook
   command. The spec carries it as D63, which renames the flags `--remind`
   and `--check`.
7. `docs/adr/0007-publishing-the-rules.md` — `stele publish` sends `stele.md`
   to a git repository as a merge request; network code only in
   `internal/publish`, tokens only to github.com and gitlab.com. It amends D53
   and D40. The spec carries it as D64–D66.
8. `docs/adr/0008-no-backstop-key.md` — `backstop` is removed from `stele.json`;
   `--remind` and `--check` replace it. The spec carries it as D67.
9. `docs/adr/0009-agent-adapter-interface.md` — everything specific to one
   coding agent is behind `adapter.Adapter`, in `internal/adapter/<agent>`. The
   spec carries it as D68.
10. `docs/adr/0010-a-local-log-of-what-stele-does.md` — `.stele/log.jsonl`:
    one signed line per thing stele did (rules loaded, write denied, write
    checked, `stele check` run), rule ids, paths and counts only, never sent.
    The spec carries it as D69.
11. `docs/adr/0011-rules-from-another-git-repository.md` — a built-in `git`
    source reads rules from another repository at `stele compile`, `stele
    check` and `stele init`, never in the hook; the commit is pinned in
    `stele.lock`. It amends ADR-0004 and ADR-0007, which kept compile and
    check offline (they may now fetch a listed repository; the hook still
    never does), and D45, D53, D62 and D66. The spec carries it as D71–D72.
12. `docs/adr/0012-a-second-agent-codex.md` — Codex, a second agent:
    `stele hook --agent=<name>`, an agent registry, one tool call touching
    several files (`Event.Edits`), an agent that cannot ask, and the rules in a
    marked section of the user's `AGENTS.md`. It amends ADR-0005 and ADR-0009.
    The spec carries it as D73–D77.
13. `docs/adr/0013-external-plugins.md` — external sources and checkers:
    protocol 1, the registry in `stele.json`, how a plugin is found and run,
    and where (sources at compile, check and init; checkers at check only;
    never the hook or stats). It amends ADR-0002 and ADR-0004, and D42, D43,
    D45, D58 and D71. The spec carries it as D78–D81.
14. `docs/adr/0014-sinks-push-and-status.md` — sinks in `stele.json`,
    `stele push` (the log's signed lines as written, a sealed cursor per sink,
    at least once), `stele check --push` (in CI one record of the run's
    counts, never the report), `share_briefs` reserved, and `stele status`.
    It amends ADR-0003 and ADR-0010, and D53. The spec carries it as D82–D85.
15. `docs/adr/0015-stele-rules-for-other-tools.md` — `stele rules
    [--format text|json]` prints the compiled rules (id, source, scope, body)
    for other tools, read as the hook reads them: no process started, never
    `stele.md` or `stele.json` written. The spec carries it as D86.
16. `docs/adr/0016-project-dir-in-plugin-requests.md` — every plugin request
    but `describe` carries `project_dir`, the project's folder; `root` stays
    as its old name. It amends ADR-0013 and ADR-0014, and D53, D78 and D82.
    The spec carries it as D87.
17. `docs/adr/0017-a-json-schema-for-stele-json.md` — `schema/stele.v1.json`
    is the JSON Schema of `stele.json`, for editors, served at
    `https://stele.lictorlabs.com/schema/stele.v1.json`; stele ignores
    `"$schema"`, and tests keep the schema's keys equal to the ones stele
    reads. The spec carries it as D88.
18. `docs/adr/0018-keys-stele-doesnt-know.md` — `stele init` and `stele
    status` note each top-level `stele.json` key stele doesn't know, never
    as an error; `stele publish` refuses an unknown key under `publish`.
    It amends ADR-0017. The spec carries it as D89.
19. `docs/adr/0019-opengrep-and-trivy-checkers.md` — two checker plugins
    ship with stele, `stele-checker-opengrep` (a rules file in the project,
    optional ids) and `stele-checker-trivy` (scanners, optional severity),
    released with it; they find the scanner on `PATH` outside the project
    and send nothing, though Trivy downloads its own database. The spec
    carries it as D90.
20. `docs/architecture.md` — how the spec is built: packages, types, contracts.

`stele-design-doc.md` is the discussion document: reasoning and vocabulary, not
decisions for v1.

`docs/adr/0001-guideline-storage-format.md` is **superseded** by ADR-0002. Its
format (guidelines with owner, version, severity, `SEC-001` IDs) is a post-v1
enterprise idea. Do not implement it or use it as a v1 format.

## Rules that are easy to break

- stele never writes to `stele.md` or `stele.json` (D38).
- Only source plugins open rule files (D40). Everything else reads `index.json`.
  The one exception: `stele publish` reads `stele.md` from git to copy it (D64).
  The git source (`internal/source/gitrepo`) is a source: it reads a shared
  rule file through `internal/gitexec` (D71).
- External plugins never run on the hot path: not in `stele hook`, not in
  `stele mcp` (D45). Nor does the `git` source, nor anything that starts git:
  only `internal/gitexec` and `cmd/stele/check.go` run git, and the hook and
  `stele stats` never reach them (D71). Only `internal/pluginexec` starts a
  plugin, and only `cmd/stele/sources.go` hands it to `internal/plugin`
  (D79).
- Only `internal/adapter/<agent>`, `cmd/stele/agents.go` and
  `internal/install/agent_test.go` (the install tests, which check the real
  adapter's output) import an agent adapter; nothing else names Claude Code's
  or Codex's events, tools or files (ADR-0009, ADR-0012).
- Only plugins listed in `stele.json` and found on `PATH` run, never one inside
  the repository (D43).
- Nothing leaves the machine except through a sink listed in `stele.json`, and
  only from `stele push` or `stele check --push` (D53), or from `stele publish`
  to the `repo` in `stele.json` (D66). The `git` source fetches rules in and
  sends nothing of the project (D71). A sink gets the log's signed lines as
  written (ids, paths, counts), or in CI one record of a check run's counts,
  never a check report or code (D83, D84).
- A new `stele.json` key goes in `enforce.Keys()` (top level) or a decoded
  struct, and in `schema/stele.v1.json`, in the same change: the schema
  tests fail otherwise (D88). A top-level key missing from `enforce.Keys()`
  is noted as unknown by `stele init` and `stele status` (D89).
