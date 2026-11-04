package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/lock"
)

// runInit sets stele up in the current directory: the agents' hooks,
// .stele/ and, when there is none, stele.md (spec §6, ADR-0004). It then
// compiles, which writes stele.lock and the rules file each agent loads at
// launch (#68). Run again, it writes nothing unless the hooks carry another
// stele version or the rules changed. --remind and --check switch the two
// write hooks (ADR-0006); a switch not given keeps what is installed.
// --agent names the agents to set up (ADR-0012); with none, those already
// set up here, or the default agent.
func runInit(args []string, stdout, stderr io.Writer) int {
	var set install.Set
	var names agentList
	root, ok := installRoot("init", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.Var(switchFlag{&set.Remind}, "remind", "before a write, deny it to show the rules: on or off")
		fs.Var(switchFlag{&set.Check}, "check", "after a write, check it against the rules: on or off")
		fs.Var(&names, "agent", "the agent to set up: "+knownAgents()+"; repeat it, or separate names with commas")
	})
	if !ok {
		return exitCantRun
	}
	chosen, err := chooseAgents(names, root)
	if err != nil {
		fmt.Fprintf(stderr, "stele init: %v\n", err)
		return exitCantRun
	}
	notRoot("init", root, chosen, stderr)
	var changes, notes []string
	for _, a := range chosen {
		c, n, ierr := install.Init(a, root, binaryVersion(), set)
		changes, notes = appendNew(changes, c...), appendNew(notes, n...)
		if ierr != nil {
			err = ierr
			break
		}
	}
	if err == nil {
		c, n := initCompile(root)
		changes, notes = appendNew(changes, c...), appendNew(notes, n...)
		for _, a := range chosen {
			notes = appendNew(notes, probeVersion(a, root)...)
		}
		notes = appendNew(notes, otherAgents(root, chosen)...)
	}
	code := report("init", changes, notes, err, stdout, stderr)
	if code == exitOK {
		for _, a := range chosen {
			if sw, ok := install.InstalledSwitches(a, root); ok {
				label := "hooks: "
				if len(chosen) > 1 || a.Name() != agents[0].Name() {
					label = a.Words().Agent + " hooks: "
				}
				fmt.Fprintln(stdout, label+sw.String())
			}
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

// agentList is the --agent flag: names, repeated or separated by commas.
type agentList []string

func (l *agentList) String() string { return strings.Join(*l, ",") }

func (l *agentList) Set(s string) error {
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			*l = append(*l, n)
		}
	}
	return nil
}

// knownAgents lists the agents stele knows, for messages.
func knownAgents() string {
	var names []string
	for _, a := range agents {
		names = append(names, a.Name())
	}
	return strings.Join(names, ", ")
}

// chooseAgents returns the agents names asks for, each once, in the
// registry's order; with none, the agents whose stele hooks are installed
// under root, or else the default agent.
func chooseAgents(names agentList, root string) ([]adapter.Adapter, error) {
	if len(names) == 0 {
		return installedAgents(root), nil
	}
	want := map[string]bool{}
	for _, n := range names {
		if _, ok := agentNamed(n); !ok {
			return nil, fmt.Errorf("no agent %q: stele knows %s", n, knownAgents())
		}
		want[n] = true
	}
	var chosen []adapter.Adapter
	for _, a := range agents {
		if want[a.Name()] {
			chosen = append(chosen, a)
		}
	}
	return chosen, nil
}

// installedAgents are the agents whose stele hooks are installed under root,
// or the default agent when none is.
func installedAgents(root string) []adapter.Adapter {
	var out []adapter.Adapter
	for _, a := range agents {
		if _, ok := install.InstalledSwitches(a, root); ok {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return agents[:1]
	}
	return out
}

// otherAgents says how to set stele up for an agent whose settings directory
// is here, when init did not and its hooks are not installed.
func otherAgents(root string, chosen []adapter.Adapter) []string {
	var notes []string
	for _, a := range agents {
		if slices.ContainsFunc(chosen, func(c adapter.Adapter) bool { return c.Name() == a.Name() }) {
			continue
		}
		if _, ok := install.InstalledSwitches(a, root); ok {
			continue
		}
		dir := filepath.Dir(a.SettingsFile())
		if info, err := os.Stat(filepath.Join(root, dir)); err == nil && info.IsDir() {
			notes = append(notes, fmt.Sprintf("%s/ is here: run stele init --agent=%s to set stele up for %s too",
				filepath.ToSlash(dir), a.Name(), a.Words().Agent))
		}
	}
	return notes
}

// appendNew appends the lines of add that list does not hold yet: two
// agents' installs share stele.md and .stele/.
func appendNew(list []string, add ...string) []string {
	for _, s := range add {
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

// initCompile compiles the rules init set up. A failure is a note, not an
// error: the hooks are installed, and the next stele compile writes the rest.
func initCompile(root string) (changes, notes []string) {
	var files []string
	for _, a := range activeAgents(root) {
		files = append(files, a.RulesFile())
	}
	later := "run stele compile to write " + strings.Join(files, " and ")
	key, err := loadKey()
	if err != nil {
		return nil, []string{fmt.Sprintf("%v: %s", err, later)}
	}
	r, err := compile.Compile(context.Background(), root, sources(), generated(root), uuid.NewString, key)
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
	notes = generatedNotes(root, r.Files)
	if ge != nil {
		notes = append(notes, fmt.Sprintf("%v: %s", ge, later))
	}
	return changes, notes
}

// runUninstall removes exactly what init wrote in the current directory:
// for the agents --agent names, or else for every agent set up here.
// .stele/ goes with the last agent's hooks.
func runUninstall(args []string, stdout, stderr io.Writer) int {
	var names agentList
	root, ok := installRoot("uninstall", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.Var(&names, "agent", "the agent to remove stele from: "+knownAgents()+"; repeat it, or separate names with commas")
	})
	if !ok {
		return exitCantRun
	}
	chosen, err := chooseAgents(names, root)
	if err != nil {
		fmt.Fprintf(stderr, "stele uninstall: %v\n", err)
		return exitCantRun
	}
	notRoot("uninstall", root, chosen, stderr)
	var changes, notes []string
	for _, a := range chosen {
		var others []adapter.Adapter
		for _, o := range agents {
			if o.Name() != a.Name() {
				others = append(others, o)
			}
		}
		c, n, uerr := install.Uninstall(a, root, others)
		changes, notes = appendNew(changes, c...), appendNew(notes, n...)
		if uerr != nil {
			err = uerr
			break
		}
	}
	return report("uninstall", changes, notes, err, stdout, stderr)
}

// installRoot parses init's or uninstall's flags and returns the current
// directory. An agent reads its settings file from the directory it starts
// in, so both work where they run, never on an ancestor a root search would
// find; notRoot says so. define, when not nil, adds the command's own flags.
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
	return root, true
}

// notRoot notes that root is not the repository root, when it is not: each
// of chosen reads its settings from the directory it starts in.
func notRoot(name, root string, chosen []adapter.Adapter, stderr io.Writer) {
	// A directory holding .git is a repository root, a worktree's or
	// submodule's too, whatever is above it (#42).
	parent := filepath.Dir(root)
	if parent == root || holdsGit(root) {
		return
	}
	up, ok := walkUp(parent, rootMarkers...)
	if !ok {
		return
	}
	for _, a := range chosen {
		fmt.Fprintf(stderr, "stele %s: this is not the repository root (%s); %s reads %s from the directory it starts in\n",
			name, up, a.Words().Agent, filepath.ToSlash(a.SettingsFile()))
	}
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
