# Make the engine ready for an org-wide dashboard

* Status: accepted
* Date: 2026-10-11
* Builds on: [ADR-0002](0002-engine-and-extension-points.md)

## Context and Problem Statement

After v1 there will be a dashboard: a website where a team or an organization
manages stele across many repositories. It will be open source, with some
features paid. It should be able to show and manage:

* plugins, parsers and extractors (sources, checkers);
* hooks, subagents and skills (agent adapters);
* guidelines and rules, org-wide;
* usage: which rules are selected, ignored or broken, and where.

We are not building it now. But some engine decisions are cheap today and
expensive after v1 ships, because they fix file formats and identities that
repositories will commit. This ADR makes those decisions so the dashboard can be
added later without a migration.

## Considered Options

* **Option 1:** The dashboard is an ordinary client of the engine's public,
  versioned formats. Data leaves a machine only through an opt-in plugin.
* **Option 2:** The engine reports to a stele service directly (built-in
  telemetry and a private API).
* **Option 3:** Decide nothing now; design the dashboard when we build it.

## Decision Outcome

Chosen option: **Option 1**. It keeps the engine fully useful offline and free of
any service, lets the dashboard be open source because it uses nothing private,
and leaves paid features a clear place to live: in plugins and in the dashboard,
never in the engine.

### 1. The dashboard uses only public formats

The dashboard reads and writes what anyone can: `stele.json`, `stele.lock`, the
log, the `stele check` report, `stele status`, and the plugin protocol. The
engine has no private API for it. Anything the dashboard can do, a script can do.

### 2. Every format carries a version

| Format | Version field |
| :-- | :-- |
| Plugin protocol | `"protocol": 1` (already, ADR-0002) |
| `stele.lock` | `"version": 1` (already) |
| Log entry, `.stele/log.jsonl` | `"v": 1` — **new** |
| `stele check --format json` | `"v": 1` — **new** |
| `stele status --format json` | `"v": 1` — **new** |

A dashboard will read data written by many stele versions at once. Without a
version on every record it cannot tell an old shape from a broken one.

### 3. Every repository has a stable id

`stele.lock` gains a `project` field: a UUID minted by the first compile and
committed. Rule ids are only unique within one repository; org-wide numbers need
to know which repository a record came from. It lives in the lock because the
lock is already stele-written and committed (D24). Log entries and check reports
carry it.

### 4. A source may supply its own rule ids

Today every id is a UUID minted by `stele.lock`. That is right for `stele.md`, but
a rule managed centrally must have **the same id in every repository**, or "rule X
was broken in 40 repositories" cannot be counted.

So a source may return an id with a rule. Reconciliation then uses it as-is,
before the hash and position passes. `stele.md` never supplies one. A supplied id
must be unique across the whole corpus, like any other; a clash is a loud error.

This is also what paid extraction needs: rules extracted from a guideline can
carry ids the server owns, so they stay stable however the prose is re-extracted.

### 5. Org-wide rules arrive through the Source slot

Rules managed in the dashboard reach a repository through a source plugin, for
example `stele-source-<org>`. There is no second path for rules to enter a
repository. That reuses everything already decided: the lock, ids, the
`stele.json` allow-list, and "external plugins never run on the hot path".

### 6. A fourth slot: sinks. Data leaves only by opt-in

| Slot | Does | Ships | Loads as |
| :-- | :-- | :-- | :-- |
| **Sink** | Sends log entries and check reports somewhere else. | none in v1 | `stele-sink-<name>`, external only |

* **Nothing leaves a machine unless a sink is listed in `stele.json`.** No sink,
  no network. The log stays local by default, as D10 says.
* **Sinks run only when asked:** `stele push` sends new log entries, and
  `stele check --push` sends the report it just produced. Never from the hook or
  the MCP server (D45).
* **Briefs stay home by default.** A brief is the agent's own words and may
  describe private work. A sink receives it only with `"share_briefs": true`.
  Rule ids, excerpts, counts and paths are sent; code never is (D10).
* `stele push` remembers how far it got per sink, in `.stele/`, so it never
  sends a record twice.

Sinks are external only because every real destination — the dashboard, a data
warehouse, a SIEM — is a network call, and the engine stays offline.

### 7. stele can describe its own install

`stele status --format json` reports, for one repository:

* the stele binary version and the version `init` stamped;
* installed agent adapters and what each installed: hook, MCP registration,
  subagent, skills;
* configured plugins, whether each was found on `PATH`, and the version its
  `describe` reports;
* each source's fingerprint and rule count, and whether the index is fresh;
* the `project` id.

`describe` (ADR-0002) gains a `version` field for this. `stele status` runs
`describe` on external plugins, so like `compile` it may run them, and like
`compile` it is never on the hot path.

This is how a dashboard sees and manages plugins, parsers, extractors, hooks and
skills across many repositories. It changes them the same way a human does: by
proposing a reviewed change to `stele.json`, never by writing to a repository
behind the team's back.

### What stays the same

* The engine is fully useful with no dashboard and no network.
* Agent adapters stay compiled in. The dashboard sees them through
  `stele status`; it never installs them.
* Only plugins listed in `stele.json` and found on `PATH` run (D43).
* stele never writes `stele.md` or `stele.json` (D38).

### Free and paid

Per ADR-0002: free if a single developer would want it. The sink protocol,
`stele status`, the version fields and the `project` id are engine features, so
they are free. A self-hosted open-source dashboard showing one team's repositories
is free. Org-wide governance, centrally managed rules, extraction, and audit
history are paid.

### Positive Consequences

* The dashboard can be added after v1 with no change to any committed file
  format.
* Org-wide numbers are possible: repositories and centrally managed rules have
  ids that agree across machines.
* Privacy is a default, not a setting someone has to find.
* An organization can send the same data to its own tools instead of ours.

### Negative Consequences

* Three new versioned formats are public API from v1 on.
* A source-supplied id bypasses position matching. If the server reassigns an
  id, the rule's history splits. That is the server's responsibility, and
  compile reports every changed id.
* A repository copied from a template keeps the template's `project` id. Two
  repositories then look like one. A `stele compile --new-project` escape hatch
  is needed before the dashboard ships.
* `stele status` runs external executables to ask their version. It is a
  command a human or CI runs, never the hook, but it is one more place plugins
  run.

## Rejected options

* **Built-in telemetry and a private API (Option 2):** the free engine would
  depend on our service, the dashboard could not be open source honestly, and
  every user would have to trust us with data before getting any value.
* **Decide later (Option 3):** the `project` id, source-supplied ids and log
  versioning all land in files that v1 users commit. Adding them after v1 means
  a migration in every repository.
