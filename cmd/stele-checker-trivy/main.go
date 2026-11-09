// stele-checker-trivy is a stele checker plugin (protocol 1) that runs
// Trivy on the project for vulnerable dependencies, secrets and
// misconfigured infrastructure code (ADR-0019):
//
//	scope: ["**/package-lock.json", "**/go.mod", "**/Dockerfile", "**/*.tf"]
//	check: trivy
//	scanners: [vuln, misconfig]
//	severity: [HIGH, CRITICAL]
//
//	No dependency with a known high or critical vulnerability.
//
// stele check starts it, one request on stdin, one answer on stdout. It
// finds trivy on PATH, never inside the project, and runs it with no shell,
// outside the project. Trivy downloads its own vulnerability database and
// checks; this plugin sends nothing.
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
