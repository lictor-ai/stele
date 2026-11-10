# Opengrep and Trivy checkers that ship with stele

* Status: accepted
* Date: 2026-11-09

## Context and Problem Statement

A rule in `stele.md` can say "no SQL built from strings" or "no dependency
with a known critical vulnerability", but the built-in `regex` checker can't
check either. Opengrep (a fork of Semgrep) checks code with rules written as
code patterns; Trivy finds vulnerable dependencies, secrets and
misconfigured infrastructure code. Both are well known, run offline once
installed, and write JSON. ADR-0013 lets a checker plugin run them; #119
asks for two that ship with stele, so a team doesn't have to write them.

Four things have to be settled: where the plugins live, the keys a rule
gives them, what goes to the network, and where a finding with no line goes.

## Considered Options

* **Where they live.** In this repository, as `cmd/stele-checker-opengrep`
  and `cmd/stele-checker-trivy`, released with stele; or in a repository of
  their own, released on their own.
* **How a rule names Opengrep rules.** A rules file in the project, with
  optional ids; a registry ruleset (`p/python`) or a URL; rules written
  inline in `stele.md`.
* **Trivy's keys.** The scanners and severities, as Trivy names them; or
  every Trivy flag, passed through.
* **A finding with no line** (a vulnerable package in a lockfile). Line 1
  of the file; the line that names the package; dropped.

## Decision Outcome

**Where they live** (D90). In this repository, `cmd/stele-checker-opengrep`
and `cmd/stele-checker-trivy`, built by `.goreleaser.yaml` for stele's
platforms, each in an archive of its own on the release. Homebrew and Scoop
install stele only. They are ordinary external checkers (ADR-0013): a
project lists them under `checkers` in `stele.json`, stele finds them on
`PATH` outside the project, and they run only in `stele check`, never in
the hook. What they share, the checker side of protocol 1, is
`internal/extcheck`; stele itself never imports it.

**Opengrep's keys.**

```
scope: ["**/*.py"]
check: opengrep
rules: .opengrep/sql.yml
ids: [python-sqli]

No SQL built from strings: use parameterised queries.
```

* `rules` (required): an Opengrep rules file in the project, relative to its
  root, with no `..`, a regular file that stays inside the project once
  links are followed. No registry name and no URL: the rules are reviewed
  like code and Opengrep fetches nothing.
* `ids` (optional): the Opengrep rule ids that count, as a list or one
  value. Without it, every rule in the file counts. `validate` refuses an
  id the file doesn't hold, found by a look at its `id:` lines (YAML) or
  `"id": "…"` (JSON), not a full parse: an `id` nested in a rule's metadata
  counts as one too.
* `message` (optional): what a violation says instead of Opengrep's message,
  as for `regex`.

Opengrep, like Semgrep, puts the rules file's folder in front of each id
(`C.Users.me.project..opengrep.python-sqli`); a finding counts for an id
when its `check_id` is the id or ends with `.` and the id. Opengrep runs
once per rules file, with the files stele sent, as absolute paths, on as
few command lines as fit (32,767 characters on Windows). Its column counts
bytes; the checker takes the column from the byte offset Opengrep gives,
counting characters, as the protocol says. An Opengrep error that names a
file stele sent, such as a partial parse, makes that file skipped, so
`stele check` reports it. An error about a rule that counts, such as a rule
Opengrep is too old to run (`IncompatibleRule`, at level `info`, with exit
status 0), or about the rules file as a whole, fails the check: the files
were never checked against it. So does any other error, an exit status
other than 0 or 1, or output that isn't Opengrep's JSON. Opengrep runs with
`--disable-nosem`: a `# nosemgrep` comment in the code checked doesn't
switch off the rule, since that code is what is under review.

**Trivy's keys.**

```
scope: ["requirements.txt", "**/package-lock.json", "**/Dockerfile"]
check: trivy
scanners: [vuln, misconfig]
severity: [HIGH, CRITICAL]

No dependency with a known high or critical vulnerability.
```

* `scanners` (required): one or more of `vuln`, `secret` and `misconfig`,
  Trivy's own names.
* `severity` (optional): one or more of `UNKNOWN`, `LOW`, `MEDIUM`, `HIGH`
  and `CRITICAL`, in any case. Without it, every severity counts.
* `message` (optional), as for Opengrep.

No other Trivy flag can be set: a rule says what to look for, not how
Trivy runs. Trivy runs `trivy fs` on the whole project, once per set of
scanners and severities, and only findings in the files stele sent count:
`stele check` without `--all` sends the changed files, so an unchanged
lockfile isn't reported until it changes or `--all` runs. Trivy's own
allow rules still apply: its secret scanner skips some paths, such as
test folders, and a secret there isn't reported even when the stele rule's
scope covers the file.

**Where a finding with no line goes.** Trivy gives a secret and most
misconfigurations a line, and a vulnerable package none. The package goes on
the first line of its file that names both the package and its version,
else the first that names the package, at the column where the name starts;
else on line 1, column 1. A misconfiguration with no line, such as a
Dockerfile with no `USER`, goes on line 1.

**The network.** stele sends nothing, as before (D53). The plugins open no
connection and import no network package. Opengrep is given only a local
rules file, so it fetches no rules, and runs with `--disable-version-check`,
so it doesn't ask for a newer version; it has no metrics. Trivy runs with
`--offline-scan`, so it sends nothing to identify a dependency, and with
`TRIVY_SKIP_VERSION_CHECK` and `TRIVY_DISABLE_TELEMETRY` set; but it
downloads its own vulnerability database, for `misconfig` its checks, and
when it scans a Java archive (`.jar`, `.war`) its Java database, each when
its copy is missing or out of date. The checker doesn't pass
`--skip-java-db-update`: a first scan of a jar would then fail. A user who
wants no download sets `TRIVY_SKIP_DB_UPDATE` and
`TRIVY_SKIP_JAVA_DB_UPDATE`, which reach Trivy. That is Trivy's network, as fetching is git's for the git source
(D71): it brings data in and sends nothing of the project.

**Running a scanner** follows D43 and D80, as for a plugin: the scanner is
found only in `PATH` entries that are absolute and outside the project and
the repository that holds it, on Windows only as an `.exe` or `.com`; it
runs with no shell, by absolute path, in the plugin's own working directory
(stele's, outside the project), with `PATH` and the loader paths cut the
same way. So a `trivy.yaml` or `.trivyignore` in the project is not read. A
scanner that is missing is a plugin error, never "no violations".

### Positive Consequences

* A rule in `stele.md` can be checked by Opengrep or Trivy with a few keys,
  and nothing to write.
* The plugins ship and are versioned with stele, and are tested here,
  against output recorded from the real tools.
* Opengrep rules live in the project, reviewed like code, and checking
  them needs no network.

### Negative Consequences

* Two more binaries in each release.
* Trivy scans the whole project each time, however few files stele sends.
* The Opengrep id check is a look at the file, not a parse.
* Trivy needs the network the first time, and when its database is out of
  date, unless the user skips the update (`TRIVY_SKIP_DB_UPDATE`).
