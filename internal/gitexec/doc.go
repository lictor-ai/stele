// Package gitexec runs git for the commands that reach a remote: stele
// publish, and the git source at stele compile, stele check and stele init
// (ADR-0011). It is, with stele check's own git in cmd/stele/check.go, the
// only place stele starts git, and stele hook and stele stats never reach it
// (D45, D70).
//
// Every git it runs is found on PATH outside the project, with a PATH that
// holds nothing from inside the project, no prompt, and flags that keep a
// repository's config, a transport command or a local path from running
// anything. A "repo" from stele.json is checked by ValidateRepo before git
// sees it. git does the network: this package imports net/url only to check
// a URL.
package gitexec
