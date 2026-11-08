package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/printable"
	"github.com/lictor-ai/stele/internal/rulelist"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// runRules prints the rules the hook enforces, for other tools (D86,
// ADR-0015): the index, brought up to date from the built-in sources as the
// hook does, so it may write .stele/, stele.lock and an agent's rules file,
// never stele.md or stele.json (D38). It opens no rule file (D40), starts no
// process and runs no plugin (D45, D79). It exits 0 when it printed, 2 when
// it could not run.
func runRules(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stele rules", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output `format`: text or json")
	asJSON := flags.Bool("json", false, "the same as --format json")
	if err := flags.Parse(args); err != nil {
		return exitCantRun
	}
	cantRun := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stele rules: "+format+"\n", a...)
		return exitCantRun
	}
	if *format != "text" && *format != "json" {
		return cantRun("--format is text or json, not %q", *format)
	}
	if *asJSON {
		if *format == "text" && formatSet(flags) {
			return cantRun("--json is --format json, not --format text")
		}
		*format = "json"
	}
	if flags.NArg() > 0 {
		return cantRun("takes no arguments, got %q", flags.Arg(0))
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}

	if !usesStele(root) {
		// No rules, and nothing written: not even a key.
		committed, _ := lock.Load(root)
		l := rulelist.Build(index.Index{Project: committed.Project}, lock.File{})
		var err error
		if *format == "json" {
			err = printRulesJSON(stdout, l)
		} else {
			_, err = fmt.Fprintf(stdout, "no stele.md or stele.json in %s: no rules\n", showPath(root))
		}
		if err != nil {
			return cantRun("writing the rules: %v", err)
		}
		return exitOK
	}

	key, err := loadKey()
	if err != nil {
		return cantRun("%v", err)
	}
	idx, note, err := freshIndex(root, key)
	if err != nil {
		return cantRun("%v", err)
	}
	committed, lerr := lock.Load(root)
	l := rulelist.Build(idx, committed)
	if note != "" {
		l.Notes = append(l.Notes, note)
	}
	if lerr != nil {
		l.Notes = append(l.Notes, fmt.Sprintf("%s: %v", lock.FileName, lerr))
	}

	printList := printRules
	if *format == "json" {
		printList = printRulesJSON
	}
	if err := printList(stdout, l); err != nil {
		return cantRun("writing the rules: %v", err)
	}
	if *format == "json" {
		return exitOK
	}
	for _, n := range l.Notes {
		fmt.Fprintf(stderr, "stele rules: %s\n", printable.Clean(n))
	}
	for _, s := range l.Missing {
		fmt.Fprintf(stderr, "stele rules: the rules of %s are not compiled on this machine: run stele compile\n", printable.Clean(sourceLabel(s)))
	}
	return exitOK
}

// formatSet reports whether --format was given.
func formatSet(flags *flag.FlagSet) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "format" {
			set = true
		}
	})
	return set
}

// printRulesJSON prints l as JSON. An error is a stdout that failed: the
// rules were not printed.
func printRulesJSON(w io.Writer, l rulelist.List) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// printRules prints one line per rule, for a person: id, source and place,
// scope, excerpt; then a count. Text from a rule file or stele.lock is
// cleaned: it can neither rewrite the terminal nor forge a line. It builds
// the text first and writes it at once, so an error is a stdout that
// failed.
func printRules(w io.Writer, l rulelist.List) error {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, r := range l.Rules {
		scope := "every file"
		if len(r.Scope) > 0 {
			scope = strings.Join(r.Scope, ", ")
		}
		fmt.Fprintf(tw, "%s\t%s rule %d\t%s\t\"%s\"\n", printable.Clean(r.ID), printable.Clean(sourceLabel(r.Source)), r.Source.Rule, printable.Clean(scope), printable.Clean(check.Excerpt(r.Text)))
	}
	tw.Flush()
	fmt.Fprintln(&b, plural(len(l.Rules), "rule"))
	_, err := w.Write(b.Bytes())
	return err
}

// sourceLabel names a source for a person: stele.md, a git source's
// repository and file, a plugin's name.
func sourceLabel(s rulelist.Source) string {
	switch s.Kind {
	case rulelist.KindSteleMD:
		return s.File
	case rulelist.KindGit:
		return strings.TrimPrefix(s.Name, gitrepo.Name+":")
	}
	return s.Name
}
