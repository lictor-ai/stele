// stele-checker-opengrep is a stele checker plugin (protocol 1) that runs
// Opengrep with the rules file a stele rule names (ADR-0019):
//
//	scope: ["**/*.py"]
//	check: opengrep
//	rules: .opengrep/sql.yml
//	ids: [python-sqli]
//
//	No SQL built from strings: use parameterised queries.
//
// stele check starts it, one request on stdin, one answer on stdout. It
// finds opengrep on PATH, never inside the project, and runs it with no
// shell, outside the project, with a rules file from the project only: no
// registry, no URL.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/lictor-ai/stele/internal/extcheck"
)

// version is set at release with -ldflags "-X main.version=v1.5.0".
var version = ""

func binaryVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(extcheck.Serve(ctx, binaryVersion(), checker{}, os.Stdin, os.Stdout))
}
