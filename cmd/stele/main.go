// Command stele enforces coding guidelines for coding agents.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// Exit codes. check uses all three (spec §8); the other commands use 0 and 2.
const (
	exitOK      = 0
	exitFailed  = 1
	exitCantRun = 2
)

type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{
	"init":      {"set stele up here: Claude Code hooks, stele.md, .stele/", runInit},
	"uninstall": {"remove exactly what init wrote", runUninstall},
	"compile":   {"compile every source into the index", runCompile},
	"check":     {"validate the rules and run checkers on changed files", runCheck},
	"status":    {"describe the install", notImplemented("status")},
	"push":      {"send new log entries to every listed sink", notImplemented("push")},
	"mcp":       {"run the MCP server on stdio", notImplemented("mcp")},
	"hook":      {"read a Claude Code hook payload on stdin, decide", runHook},
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
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, commands[name].summary)
	}
}

func notImplemented(name string) func([]string, io.Writer, io.Writer) int {
	return func(_ []string, _, stderr io.Writer) int {
		fmt.Fprintf(stderr, "stele %s: not implemented yet\n", name)
		return exitCantRun
	}
}
