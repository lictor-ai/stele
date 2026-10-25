package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/sign"
	"github.com/lictor-ai/stele/internal/source"
)

// runCompile reads every source, then writes stele.lock and the index when
// they changed, and prints how each rule got its id. It then checks the
// rules with the built-in checkers, as stele check does, and exits 1 when one
// is invalid (#54). The files are written anyway: an invalid rule keeps its
// id, and the valid rules are enforced.
func runCompile(args []string, stdout, stderr io.Writer) int {
	root, ok := setup("compile", args, stderr)
	if !ok {
		return exitCantRun
	}
	key, err := loadKey()
	if err != nil {
		fmt.Fprintf(stderr, "stele compile: %v\n", err)
		return exitCantRun
	}
	r, err := compile.Compile(context.Background(), root, sources(), generated(), uuid.NewString, key)
	if err != nil {
		fmt.Fprintf(stderr, "stele compile: %v\n", err)
		return exitCantRun
	}
	printCompile(stdout, r)
	ps := check.Validate(r.Index, plugin.BuiltinCheckers())
	if len(ps) == 0 {
		return exitOK
	}
	printProblems(stdout, r.Lock, ps)
	fmt.Fprintf(stdout, "%s in the rules: stele check fails, and the hook skips these rules, until they are fixed\n", plural(len(ps), "problem"))
	return exitFailed
}

// keyDir is where this machine's signing key lives. Tests point it at a
// temp directory.
var keyDir = sign.DefaultDir

// loadKey returns the key stele signs its generated files with (#27).
func loadKey() (sign.Key, error) {
	dir, err := keyDir()
	if err != nil {
		return nil, err
	}
	return sign.LoadKey(dir)
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

	var wrote []string
	if r.LockChanged {
		wrote = append(wrote, lock.FileName)
	}
	if r.IndexChanged {
		wrote = append(wrote, indexName)
	}
	for _, f := range r.Files {
		if f.Changed {
			wrote = append(wrote, f.Path)
		}
	}
	switch n := len(wrote); n {
	case 0:
		fmt.Fprintln(w, "up to date, nothing written")
	case 1:
		fmt.Fprintf(w, "wrote %s\n", wrote[0])
	default:
		fmt.Fprintf(w, "wrote %s and %s\n", strings.Join(wrote[:n-1], ", "), wrote[n-1])
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
