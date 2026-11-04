package codex

import (
	"regexp"
	"strconv"
)

// The Codex releases stele's hooks need, from its release notes: 0.124 runs
// hooks for apply_patch at all; 0.141 makes a block after a write hold when
// the model calls tools through code mode, which models do; 0.145 fixes how
// hook commands are quoted on Windows.
var (
	firstHooks = [3]int{0, 124, 0}
	minimum    = [3]int{0, 141, 0}
	minWindows = [3]int{0, 145, 0}
)

// versionRE reads `codex --version`: "codex-cli 0.166.0".
var versionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// versionProbe is the command whose output VersionNotes reads.
var versionProbe = []string{"codex", "--version"}

// VersionNotes says what the user should know about the Codex that `codex
// --version` printed out, on goos: nothing for one that is new enough. It
// never fails init: Codex may be installed later, or elsewhere.
func VersionNotes(out []byte, err error, goos string) []string {
	need := minimum
	needText := "0.141 or later"
	if goos == "windows" {
		need, needText = minWindows, "0.145 or later on Windows"
	}
	m := versionRE.FindSubmatch(out)
	if err != nil || m == nil {
		return []string{"codex was not found, or did not say its version: stele's Codex hooks need Codex " + needText}
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(string(m[i+1]))
	}
	got := string(m[0])
	switch {
	case less(v, firstHooks):
		return []string{"Codex " + got + " runs no hooks for apply_patch: upgrade it to " + needText + " (npm i -g @openai/codex)"}
	case less(v, need):
		return []string{"Codex " + got + " may not hold stele's block after a write: upgrade it to " + needText + " (npm i -g @openai/codex)"}
	}
	return nil
}

// less reports whether version a comes before b.
func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
