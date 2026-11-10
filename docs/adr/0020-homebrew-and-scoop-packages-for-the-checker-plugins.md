# Homebrew and Scoop packages for the checker plugins

* Status: accepted
* Date: 2026-11-10
* Amends: ADR-0019 ("Decision Outcome" / where they live: "Homebrew and
  Scoop install stele only"); spec D90

## Context and Problem Statement

ADR-0019 ships two checker plugins, `stele-checker-opengrep` and
`stele-checker-trivy`, in archives of their own on each release. stele
itself installs with one command from the Homebrew tap or the Scoop
bucket; a plugin means a download, an unpacked binary and a `PATH` to
edit, or a Go toolchain for `go install`. Each plugin also needs its
scanner, `opengrep` or `trivy`, on `PATH`, and stele to run it.

## Considered Options

* **No packages, archives only**, as ADR-0019 has it. Nothing to maintain,
  but the plugins stay harder to install than stele.
* **Packages that depend on stele, the scanner, or both.** One command
  installs everything. But stele is often installed another way (`go
  install`, a release archive): a dependency on the `stele` cask installs a
  second stele that may shadow the first on `PATH`. The scanners have
  their own install methods, and a user may already have one, of a
  version they chose.
* **Packages that depend on neither.**

## Decision Outcome

Chosen option: "packages that depend on neither", because a plugin then
installs with one command, as stele does, and changes nothing else on the
machine.

* `.goreleaser.yaml` builds a Homebrew cask and a Scoop manifest for each
  plugin, from the plugin's own archive, in stele's tap
  (`lictor-ai/homebrew-tap`, `Casks`) and bucket (`lictor-ai/scoop-bucket`),
  as for stele: `brew install lictor-ai/tap/stele-checker-opengrep`,
  `scoop install stele-checker-trivy`.
* A plugin's package installs neither stele nor its scanner. The plugin
  finds the scanner on `PATH`, outside the project, as before (ADR-0019).
* Each cask removes the macOS quarantine attribute from its binary, as
  stele's does; a failure there doesn't fail the install.
* The release archives and `go install` stay.

This amends ADR-0019, "Decision Outcome": Homebrew and Scoop install the
checker plugins too, not stele only.

### Positive Consequences

* A plugin installs with one command on macOS, Linux and Windows.
* A stele or a scanner already installed is left alone.

### Negative Consequences

* Installing a plugin doesn't install stele or the scanner: the user
  installs those too, as the README says.
* Two more casks and two more manifests in the tap and bucket on each
  release.
