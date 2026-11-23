package main

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

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

// runVersion prints this stele's version, for bug reports: stele version,
// stele --version or stele -v.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stele version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseFlags(fs, args, stdout); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "stele version: unexpected argument %q\n", fs.Arg(0))
		return exitCantRun
	}
	fmt.Fprintf(stdout, "stele %s\n", binaryVersion())
	return exitOK
}
