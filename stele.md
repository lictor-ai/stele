<!--
stele.md for stele itself: the rules a coding agent follows in this
repository. Format: README.md, "Writing stele.md". stele never edits this file.
-->

When documents disagree, follow them in this order: the v1 spec
(docs/superpowers/specs/2026-08-29-stele-v1-design.md), then ADR-0004 for
anything about v1's scope (it wins over the spec there), then ADR-0002 and
ADR-0003, then docs/architecture.md. stele-design-doc.md is discussion, not
decisions. ADR-0001 is superseded: never implement its format (owner,
version, severity, SEC-001 ids).

---

scope: ["**/*.go"]

stele never writes stele.md or stele.json (D38): no code path may create,
edit or rewrite them, not even to fix or migrate them. Only source plugins
(internal/source/...) open rule files (D40); every other package reads the
rules from the compiled index (index.Load, compile.EnsureFresh).

---

scope: ["**/*.go"]

Write the files stele generates through internal/safefile (atomic, locked,
size capped), never with os.WriteFile or os.Create. Everything stele reads
back from .stele/ is signed with this machine's key (internal/sign) and
verified before use; an unsigned or badly signed file is an error, never
trusted (D57). The key lives in the user's config directory, never in a
repository.

---

scope: ["cmd/stele/hook.go", "internal/adapter/", "internal/backstop/"]

The hook must never stop the agent by accident. stele hook always exits 0
and answers in JSON on stdout; exit 2 would block whatever the JSON says. It
denies a PreToolUse only to show rules not yet shown this turn, and never
sets permissionDecision "allow", which would skip the user's own permission
prompts. When stele itself fails, follow block_on_error (default: let the
write through with a warning). No external plugin and no network on this
path (D45).

---

scope: ["**/*_test.go"]

Tests are hermetic and pass on Linux and Windows (CI runs both). Use
t.TempDir for files, point keyDir at a temp directory instead of the user's
real key, and record Claude Code payloads under cmd/stele/testdata/hook
rather than inventing their shape. Build paths with filepath and compare
them after filepath.ToSlash, never with a hard-coded / or \.

---

scope: docs/adr/

An accepted ADR is not rewritten: a new ADR amends or supersedes it and says
which sections. Use docs/adr/adr-template.md and the next free number. When
an ADR changes v1, add it to the list in CLAUDE.md and give it a D-number in
the spec.

---

scope: /README.md

The README describes only what is built, in plain words. Every example
output must be real output of the current stele. When a behaviour changes,
update "Limits" in the same change; never remove a limit that still holds.

---

scope: ["**/*.go"]
check: regex
pattern: (?m)^\s*(import\s+)?([\w.]+\s+)?"(golang\.org/x/)?net(/[^"]*)?"\s*$
message: stele makes no network calls; do not import net or net/...

stele makes no network calls. Nothing leaves the machine except through a
sink listed in stele.json, and only from stele push or stele check --push
(D53); v1 has no sinks. Do not import net, net/http or any other net package.

---

scope: ["cmd/stele/{hook,compile,root,main,version}.go", "internal/{adapter,backstop,check,compile,enforce,index,lock,plugin,safefile,scope,session,sign,source}/**/*.go", "internal/install/{assets,install,doc}.go"]
check: regex
pattern: (?m)^\s*(import\s+)?([\w.]+\s+)?"os/exec"\s*$
message: no subprocess on the hook's path (D45)

stele hook runs on every write, so nothing it reaches may start a process:
external plugins never run on the hot path (D45), and only plugins listed in
stele.json and found on PATH ever run, never one inside the repository
(D43). os/exec belongs to stele check (cmd/stele/check.go) only.

---

scope: ["internal/**/*.go"]
check: regex
pattern: \bos\.Exit\(
message: packages under internal/ return errors; only cmd/stele exits

Packages under internal/ never exit the process or print. They return errors
and values; cmd/stele decides exit codes (0, 1, 2, spec §8) and what goes to
stdout and stderr. In the hook, an exit or a stray line on stdout would break
the JSON answer to Claude Code.
