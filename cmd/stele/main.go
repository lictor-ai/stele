// Command stele enforces coding guidelines for coding agents.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
)

// Exit codes. check and publish use all three (spec §8); the other commands,
// stats among them, use 0 and 2.
const (
	exitOK      = 0
	exitFailed  = 1
	exitCantRun = 2
)

type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

// hidden commands run but stay out of the usage text: they are not built
// yet.
var hidden = map[string]bool{"mcp": true}

var commands = map[string]command{
	"init":      {"set stele up here: an agent's hooks (--agent), stele.md, .stele/, then compile", runInit},
	"uninstall": {"remove exactly what init wrote", runUninstall},
	"compile":   {"compile every source into the index", runCompile},
	"check":     {"validate the rules and run checkers on changed files", runCheck},
	"stats":     {"how the rules are doing, from the local log", runStats},
	"status":    {"describe the install", runStatus},
	"rules":     {"print the compiled rules: id, source, scope, text", runRules},
	"push":      {"send the log's new lines to every sink stele.json lists", runPush},
	"publish":   {"send stele.md to the repository in stele.json as a merge request", runPublish},
	"mcp":       {"run the MCP server on stdio", notImplemented("mcp")},
	"hook":      {"read an agent's hook payload on stdin, decide", runHook},
	"version":   {"print stele's version", runVersion},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitCantRun
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return exitOK
	case "-v", "-version", "--version":
		return runVersion(args[1:], stdout, stderr)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "stele: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitCantRun
	}
	return cmd.run(args[1:], stdout, stderr)
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: stele <command> [flags]")
	fmt.Fprintln(w)
	names := make([]string, 0, len(commands))
	for name := range commands {
		if hidden[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, commands[name].summary)
	}
}

// parseFlags parses a command's args into fs, whose output is the command's
// stderr. Asked for help (-h, -help, --help), it prints the command's usage
// to stdout and ok is false with exit 0; on a bad flag it prints the error
// and the usage to stderr and ok is false with exit 2.
func parseFlags(fs *flag.FlagSet, args []string, stdout io.Writer) (code int, ok bool) {
	errOut := fs.Output()
	fs.Usage = func() {
		w := fs.Output()
		n := 0
		fs.VisitAll(func(*flag.Flag) { n++ })
		if n == 0 {
			fmt.Fprintf(w, "usage: %s\n", fs.Name())
			return
		}
		fmt.Fprintf(w, "usage: %s [flags]%s\n\nflags:\n", fs.Name(), operands[fs.Name()])
		fs.PrintDefaults()
	}
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	fs.SetOutput(errOut)
	switch {
	case err == nil:
		return exitOK, true
	case errors.Is(err, flag.ErrHelp):
		fs.SetOutput(stdout)
		fs.Usage()
		fs.SetOutput(errOut)
		return exitOK, false
	default:
		fmt.Fprintln(errOut, err)
		fs.Usage()
		return exitCantRun, false
	}
}

// operands names what a command takes after its flags, for its usage line.
var operands = map[string]string{"stele check": " [path ...]"}

func notImplemented(name string) func([]string, io.Writer, io.Writer) int {
	return func(_ []string, _, stderr io.Writer) int {
		fmt.Fprintf(stderr, "stele %s: not implemented yet\n", name)
		return exitCantRun
	}
}
