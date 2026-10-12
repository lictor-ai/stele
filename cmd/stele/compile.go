package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/source"
)

// runCompile reads every source, then writes stele.lock and the index when
// they changed, and prints how each rule got its id.
func runCompile(args []string, stdout, stderr io.Writer) int {
	root, ok := setup("compile", args, stderr)
	if !ok {
		return exitCantRun
	}
	r, err := compile.Compile(context.Background(), root, sources(), uuid.NewString)
	if err != nil {
		fmt.Fprintf(stderr, "stele compile: %v\n", err)
		return exitCantRun
	}
	printCompile(stdout, r)
	return exitOK
}

// runCheck validates the rules and writes nothing. It fails on a rule file
// that does not parse, an invalid stele.lock, and a lock that is out of date
// with the rules (spec §8). Running checkers on code comes with #15.
func runCheck(args []string, stdout, stderr io.Writer) int {
	root, ok := setup("check", args, stderr)
	if !ok {
		return exitCantRun
	}
	r, err := compile.Plan(context.Background(), root, sources(), uuid.NewString)
	if err != nil {
		fmt.Fprintf(stderr, "stele check: %v\n", err)
		var pe *source.ParseError
		if errors.As(err, &pe) || errors.Is(err, lock.ErrDuplicateID) || errors.Is(err, lock.ErrInvalid) {
			return exitFailed
		}
		return exitCantRun
	}
	// CI would otherwise report against ids that exist on nobody's machine.
	if r.LockChanged {
		fmt.Fprintf(stderr, "stele check: %s is out of date with the rules: run stele compile and commit %s\n",
			lock.FileName, lock.FileName)
		return exitFailed
	}
	fmt.Fprintf(stdout, "%s valid\n", plural(len(r.Index.Rules), "rule"))
	return exitOK
}

// setup parses a command's flags and finds the project root.
func setup(name string, args []string, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet("stele "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "stele %s: unexpected argument %q\n", name, fs.Arg(0))
		return "", false
	}
	root, err := findRoot(".")
	if err != nil {
		fmt.Fprintf(stderr, "stele %s: %v\n", name, err)
		return "", false
	}
	return root, true
}

// sources is every source compile reads. External sources from stele.json
// join the built-ins with the plugin registry (#14).
func sources() []source.Source { return plugin.Builtins() }

// printCompile says how each rule got its id, and what was written. Ids
// matched by position are guesses, so each is named for the reader to check.
func printCompile(w io.Writer, r compile.Result) {
	rep := r.Report
	fmt.Fprintf(w, "%s: %d matched by text, %d by position, %d new, %d dropped",
		plural(len(r.Index.Rules), "rule"), rep.ByHash, rep.ByPosition, rep.Minted, rep.Dropped)
	if rep.Supplied > 0 {
		fmt.Fprintf(w, ", %d supplied by their source", rep.Supplied)
	}
	fmt.Fprintln(w)

	if len(rep.Guessed) > 0 {
		fmt.Fprintf(w, "matched by position, a guess: check these in the %s diff\n", lock.FileName)
		for _, id := range rep.Guessed {
			for _, e := range r.Lock.Rules {
				if e.ID == id {
					fmt.Fprintf(w, "  %s  %s rule %d\n", id, e.Source, e.Position+1)
				}
			}
		}
	}
	if len(rep.Rekeyed) > 0 {
		fmt.Fprintln(w, "ids now supplied by their source:")
		for _, k := range rep.Rekeyed {
			fmt.Fprintf(w, "  %s -> %s\n", k.Old, k.New)
		}
	}

	switch {
	case r.LockChanged && r.IndexChanged:
		fmt.Fprintf(w, "wrote %s and %s\n", lock.FileName, indexName)
	case r.LockChanged:
		fmt.Fprintf(w, "wrote %s\n", lock.FileName)
	case r.IndexChanged:
		fmt.Fprintf(w, "wrote %s\n", indexName)
	default:
		fmt.Fprintln(w, "up to date, nothing written")
	}
}

// indexName is the index's path as the user sees it.
const indexName = index.Dir + "/index.json"

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
