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
6. `docs/architecture.md` — how the spec is built: packages, types, contracts.

`stele-design-doc.md` is the discussion document: reasoning and vocabulary, not
decisions for v1.

`docs/adr/0001-guideline-storage-format.md` is **superseded** by ADR-0002. Its
format (guidelines with owner, version, severity, `SEC-001` IDs) is a post-v1
enterprise idea. Do not implement it or use it as a v1 format.

## Rules that are easy to break

- stele never writes to `stele.md` or `stele.json` (D38).
- Only source plugins open rule files (D40). Everything else reads `index.json`.
- External plugins never run on the hot path: not in `stele hook`, not in
  `stele mcp` (D45).
- Only plugins listed in `stele.json` and found on `PATH` run, never one inside
  the repository (D43).
- Nothing leaves the machine except through a sink listed in `stele.json`, and
  only from `stele push` or `stele check --push` (D53).
