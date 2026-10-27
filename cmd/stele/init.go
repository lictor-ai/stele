package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/lock"
)

// runInit sets stele up in the current directory: the Claude Code hooks,
// .stele/ and, when there is none, stele.md (spec §6, ADR-0004). It then
// compiles, which writes stele.lock and the rules file Claude Code loads at
// launch (#68). Run again, it writes nothing unless the hooks carry another
// stele version or the rules changed. --remind and --check switch the two
// write hooks (ADR-0006); a switch not given keeps what is installed.
func runInit(args []string, stdout, stderr io.Writer) int {
	var set install.Set
	root, ok := installRoot("init", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.Var(switchFlag{&set.Remind}, "remind", "before a write, deny it to show the rules: on or off")
		fs.Var(switchFlag{&set.Check}, "check", "after a write, check it against the rules: on or off")
	})
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Init(agent, root, binaryVersion(), set)
	if err == nil {
		c, n := initCompile(root)
		changes, notes = append(changes, c...), append(notes, n...)
	}
	code := report("init", changes, notes, err, stdout, stderr)
	if code == exitOK {
		if sw, ok := install.InstalledSwitches(agent, root); ok {
			fmt.Fprintln(stdout, "hooks: "+sw.String())
		}
	}
	return code
}

// switchFlag is an init flag that takes on or off; unset, it stays nil.
type switchFlag struct{ p **bool }

func (f switchFlag) String() string {
	if f.p == nil || *f.p == nil {
		return ""
	}
	if **f.p {
		return "on"
	}
	return "off"
}

func (f switchFlag) Set(s string) error {
	switch s {
	case "on", "off":
		on := s == "on"
		*f.p = &on
		return nil
	}
	return errors.New("want on or off")
}

// initCompile compiles the rules init set up. A failure is a note, not an
// error: the hooks are installed, and the next stele compile writes the rest.
func initCompile(root string) (changes, notes []string) {
	later := "run stele compile to write " + agent.RulesFile()
	key, err := loadKey()
	if err != nil {
		return nil, []string{fmt.Sprintf("%v: %s", err, later)}
	}
	r, err := compile.Compile(context.Background(), root, sources(), generated(), uuid.NewString, key)
	var ge *compile.GeneratedError
	if err != nil && !errors.As(err, &ge) {
		return nil, []string{fmt.Sprintf("%v: fix it and %s", err, later)}
	}
	if r.LockChanged {
		changes = append(changes, "wrote "+lock.FileName)
	}
	for _, f := range r.Files {
		if f.Changed && !f.Foreign && ge == nil {
			changes = append(changes, "wrote "+f.Path)
		}
	}
	notes = generatedNotes(r.Files)
	if ge != nil {
		notes = append(notes, fmt.Sprintf("%v: %s", ge, later))
	}
	return changes, notes
}

// runUninstall removes exactly what init wrote in the current directory.
func runUninstall(args []string, stdout, stderr io.Writer) int {
	root, ok := installRoot("uninstall", args, stdout, stderr, nil)
	if !ok {
		return exitCantRun
	}
	changes, notes, err := install.Uninstall(agent, root)
	return report("uninstall", changes, notes, err, stdout, stderr)
}

// installRoot parses init's or uninstall's flags and returns the current
// directory. Claude Code reads .claude/settings.json from the directory it
// starts in, so both work where they run, never on an ancestor a root search
// would find; when there is one, a note says so. define, when not nil, adds
// the command's own flags.
func installRoot(name string, args []string, stdout, stderr io.Writer, define func(*flag.FlagSet)) (string, bool) {
	fs := flag.NewFlagSet("stele "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if define != nil {
		define(fs)
	}
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
	// A directory holding .git is a repository root, a worktree's or
	// submodule's too, whatever is above it (#42).
	if parent := filepath.Dir(root); parent != root && !holdsGit(root) {
		if up, ok := walkUp(parent, rootMarkers...); ok {
			fmt.Fprintf(stderr, "stele %s: this is not the repository root (%s); %s reads %s from the directory it starts in\n",
				name, up, agent.Words().Agent, filepath.ToSlash(agent.SettingsFile()))
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
