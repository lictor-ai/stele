package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lictor-ai/stele/internal/install"
)

// runInit sets stele up in the project: the Claude Code hooks, .stele/ and,
// when there is none, stele.md (spec §6, ADR-0004). Run again, it writes
// nothing unless the hooks carry another stele version.
func runInit(args []string, stdout, stderr io.Writer) int {
	root, ok := installRoot("init", args, stderr)
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Init(root, binaryVersion())
	return report("init", changes, notes, err, stdout, stderr)
}

// runUninstall removes exactly what init wrote.
func runUninstall(args []string, stdout, stderr io.Writer) int {
	root, ok := setup("uninstall", args, stderr)
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Uninstall(root)
	return report("uninstall", changes, notes, err, stdout, stderr)
}

// installRoot is setup for init: a directory with no stele.md and no
// repository is set up where it is.
func installRoot(name string, args []string, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet("stele "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "stele %s: unexpected argument %q\n", name, fs.Arg(0))
		return "", false
	}
	if root, err := findRoot("."); err == nil {
		return root, true
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "stele %s: %v\n", name, err)
		return "", false
	}
	return root, true
}

// report prints what init or uninstall did, one line each, and its notes.
func report(name string, changes, notes []string, err error, stdout, stderr io.Writer) int {
	for _, c := range changes {
		fmt.Fprintln(stdout, c)
	}
	for _, n := range notes {
		fmt.Fprintf(stderr, "stele %s: %s\n", name, n)
	}
	if err != nil {
		fmt.Fprintf(stderr, "stele %s: %v\n", name, err)
		return exitCantRun
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "up to date, nothing written")
	}
	return exitOK
}
