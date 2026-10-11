# Store guidelines as Markdown with YAML frontmatter, compiled to agent-native formats

* Status: accepted
* Date: 2026-10-11

## Context and Problem Statement

Stele enforces engineering standards in coding agents (while code is written) and in CI (before code is merged). Both need to read the same standards, and the result of every check must cite exactly which standard was broken, in which version, and who owns it.

We need to decide:

1. What we call the things we enforce.
2. How they are stored on disk.
3. How coding agents get to them.

What exists today (October 2026):

| Tool | File | Scoping field | IDs, severity, owner, version, checks |
|---|---|---|---|
| AGENTS.md (open standard, Linux Foundation / AAIF) | `AGENTS.md`, nestable | none, plain Markdown | no |
| Claude Code | `.claude/rules/*.md` | `paths` (only field read) | no |
| Cursor | `.cursor/rules/*.mdc` | `globs`, `description`, `alwaysApply` | no |
| GitHub Copilot | `.github/instructions/*.instructions.md` | `applyTo`, `excludeAgent` | no |
| Semgrep | `*.yaml` | `paths` | IDs and severity yes, but not readable as guidance by an agent |

Observations:

* The agent ecosystem has converged on **Markdown**, optionally with **YAML frontmatter** for path scoping. Agents follow prose better than structured data, and humans actually edit Markdown.
* **Path scoping is universal**, but each tool names the field differently.
* **No agent format carries stable rule IDs, severity, ownership, versioning or check definitions.** These are what make CI checks, exceptions and audits possible. This is the gap Stele fills.

## Considered Options

* **Option 1:** Markdown + YAML frontmatter, one file per guideline, rules as headings, optional inline check blocks.
* **Option 2:** Pure YAML or JSON rule files (Semgrep-style).
* **Option 3:** Plain `AGENTS.md` / `CLAUDE.md` with conventions on top.
* **Option 4:** One Markdown file per rule.

## Decision Outcome

Chosen option: **Option 1**, because it is the only one that agents read natively *and* that the engine can parse deterministically into IDs, versions and checks.

### Vocabulary

* **Guideline**: a document owned by a human or team (for example "Secrets handling"). It has an ID, a version, an owner, a scope and a rationale. It is what an architecture team writes and what an auditor asks about.
* **Rule**: one atomic, checkable statement inside a guideline, with a stable ID (for example `SEC-001`). It is what gets enforced, what a violation cites, and what an exception is granted against.

We *enforce guidelines* by *checking rules*.

### Location

```
.stele/
  guidelines/
    security/
      secrets.md
    api/
      versioning.md
```

Subdirectories are free-form. All `*.md` files under `.stele/guidelines/` are discovered recursively.

### File format (format version 1)

````markdown
---
stele: 1                      # format version, required
id: SEC-SECRETS               # guideline ID, required, unique in the repo
title: Secrets handling       # required
version: 1.2.0                # semver of this guideline, required
owner: security-architecture  # required
paths: ["**/*.py"]            # optional, default: all files
severity: high                # optional default for rules: low | medium | high | critical
source: confluence://SEC/123  # optional, where it was extracted from
---

Free Markdown rationale for the whole guideline.

## SEC-001: No hardcoded credentials
Prose that an agent reads and follows.

```stele
check: regex
pattern: '...'
blocking: true
```
````

Parsing rules:

1. **Frontmatter** holds guideline metadata. Unknown keys are kept but ignored, so plugins can add fields.
2. Each **level-2 heading** of the form `## <RULE-ID>: <title>` starts a rule. The rule's body is everything up to the next level-2 heading.
3. Rule IDs match `^[A-Z][A-Z0-9]*-[0-9]+$` and are unique across the repository.
4. A fenced block with the info string `stele` inside a rule defines its check. It is optional. Supported fields in v1:
   * `check`: name of a checker plugin (`regex`, `ast`, `llm`, …)
   * `blocking`: whether a violation fails CI (default `false`)
   * `severity`: overrides the guideline default
   * `paths`: narrows the guideline's paths for this rule
   * any other fields are passed to the checker plugin as its config
5. A rule **without** a `stele` block is guidance only. It is still delivered to agents and can still be judged by the `llm` checker.
6. Text before the first rule heading is the guideline's rationale.

### How agents get the rules: compile, don't compete

Agents are **not** asked to read `.stele/`. Adapter plugins generate each tool's native files from it:

| Target | Generated file | Scoping mapped to |
|---|---|---|
| Claude Code | `.claude/rules/stele/<id>.md` | `paths` |
| Cursor | `.cursor/rules/stele-<id>.mdc` | `globs` |
| GitHub Copilot | `.github/instructions/stele-<id>.instructions.md` | `applyTo` |
| Others | managed section in `AGENTS.md` | none |

`stele sync` writes them. Generated files carry a header saying they are generated and which guideline version they come from, and `stele check` fails if they are out of date. `stele` blocks are stripped from agent output by default, since agents need the prose, not the check config.

An MCP server will also let agents fetch the rules that apply to a given file on demand.

### Positive Consequences

* Agents get guidance in the format they already follow best, through the files their tool already loads.
* One source of truth for coding-time guidance and CI verification, so the two never disagree.
* Every violation and every exception can cite `SEC-001` in guideline `SEC-SECRETS` v1.2.0, owned by a named team. This is the basis of the audit trail.
* Git provides diffs, review and history for standards for free.
* Paid parsers (Confluence, SharePoint, PDF) have a clear target: they output this format.

### Negative Consequences

* Parsing Markdown headings is more fragile than parsing pure YAML. We mitigate this with a strict heading pattern and `stele validate`.
* Generated files appear in repositories. Some teams will want them git-ignored and generated in CI, so both modes must be supported.
* Inline check blocks may become crowded for complex checks. If that happens, a later ADR can allow `check: ref` pointing to a separate file.

## Rejected options

* **Pure YAML/JSON (Option 2):** easy to parse, but agents follow it poorly and non-developers will not edit it. It would also force us to maintain a second, prose version for agents.
* **Plain AGENTS.md (Option 3):** no IDs, versions, owners or checks, so no audit trail and nothing for CI to cite.
* **One file per rule (Option 4):** fine for engines, but it splits a standard away from its rationale and owner, and becomes hundreds of files for a real bank standard. Guidelines are the unit humans own.

## References

* Claude Code memory and `.claude/rules/`: https://code.claude.com/docs/en/memory
* GitHub Copilot custom instructions: https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-custom-instructions
* Cursor rules (`.mdc`): https://www.morphllm.com/cursor-rules-best-practices
* AGENTS.md as an open standard: https://codex.danielvaughan.com/2026/04/07/agents-md-open-standard-cross-tool-portability
