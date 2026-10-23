package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/install"
)

// runInit sets stele up in the current directory: the Claude Code hooks,
// .stele/ and, when there is none, stele.md (spec §6, ADR-0004). Run again,
// it writes nothing unless the hooks carry another stele version.
func runInit(args []string, stdout, stderr io.Writer) int {
	root, ok := installRoot("init", args, stdout, stderr)
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Init(root, binaryVersion())
	return report("init", changes, notes, err, stdout, stderr)
}

// runUninstall removes exactly what init wrote in the current directory.
func runUninstall(args []string, stdout, stderr io.Writer) int {
	root, ok := installRoot("uninstall", args, stdout, stderr)
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Uninstall(root)
	return report("uninstall", changes, notes, err, stdout, stderr)
}

// installRoot parses init's or uninstall's flags and returns the current
// directory. Claude Code reads .claude/settings.json from the directory it
// starts in, so both work where they run, never on an ancestor a root search
// would find; when there is one, a note says so.
func installRoot(name string, args []string, stdout, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet("stele "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "stele %s: unexpected argument %q\n", name, fs.Arg(0))
		return "", false
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "stele %s: %v\n", name, err)
		return "", false
	}
	fmt.Fprintf(stdout, "stele %s in %s\n", name, root)
	if parent := filepath.Dir(root); parent != root {
		if up, ok := walkUp(parent, "stele.md", "stele.json", ".git"); ok {
			fmt.Fprintf(stderr, "stele %s: this is not the repository root (%s); Claude Code reads .claude/settings.json from the directory it starts in\n", name, up)
		}
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
