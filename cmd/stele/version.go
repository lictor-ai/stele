package main

import "runtime/debug"

// version is set at release with -ldflags "-X main.version=v0.3.0". Tests
// set it too.
var version = ""

// binaryVersion is this stele's version: the ldflags value, else the module
// version from the build info (go install ...@v0.3.0), else "devel". A
// build from a commit reports a pseudo-version here; install.Released
// decides what counts as a release for the stamp and the warning.
func binaryVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
