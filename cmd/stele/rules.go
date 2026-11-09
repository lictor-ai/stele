package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
)

// rulesFormat is the version of stele rules --format json (D50, D86).
const rulesFormat = 1

// rulesReport is what stele rules --format json prints.
type rulesReport struct {
	V       int       `json:"v"`
	Project string    `json:"project"`
	Rules   []ruleOut `json:"rules"`
	Notes   []string  `json:"notes"` // what is missing from the list, as on stderr
}

// ruleOut is one compiled rule.
type ruleOut struct {
	ID     string     `json:"id"`
	Source ruleSource `json:"source"`
	Scope  []string   `json:"scope"`
	Body   string     `json:"body"`
}

// ruleSource is where a rule came from: the source's name, and for the
// project's stele.md or a shared file its path; for a shared file also the
// repository, ref and commit stele.lock pins (D72).
type ruleSource struct {
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// runRules prints the compiled rules, for a person or, with --format json,
// for another tool (D86). It reads them as the hook does: the built-in
// sources compiled when they changed, the others as this machine's index
// holds them. So it starts no process, and writes nothing but stele's own
// generated files, never stele.md or stele.json (D38), and opens no rule
// file itself (D40). It exits 0, or 2 when it could not run.
func runRules(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stele rules", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output `format`: text or json")
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
	if flags.NArg() > 0 {
		return cantRun("takes no arguments, got %q", flags.Arg(0))
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}
	rep := rulesReport{V: rulesFormat, Rules: []ruleOut{}, Notes: []string{}}
	if noRuleFiles(root) {
		if *format == "json" {
			printRulesJSON(stdout, rep)
		} else {
			fmt.Fprintf(stdout, "no stele.md or stele.json in %s: no rules\n", showPath(root))
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
	if note != "" {
		rep.Notes = append(rep.Notes, note)
	}
	committed, _ := lock.Load(root)
	rep.Notes = append(rep.Notes, absent(idx, committed)...)
	for _, n := range rep.Notes {
		fmt.Fprintf(stderr, "stele rules: %s\n", n)
	}

	rep.Project = idx.Project
	pins := make(map[string]source.Pin, len(committed.Sources))
	for _, p := range committed.Sources {
		pins[p.Source] = p
	}
	for _, e := range idx.Rules {
		scope := e.Scope
		if scope == nil {
			scope = []string{}
		}
		rep.Rules = append(rep.Rules, ruleOut{ID: e.ID, Source: ruleFrom(e.Source, idx, pins), Scope: scope, Body: e.Body})
	}
	if *format == "json" {
		printRulesJSON(stdout, rep)
	} else {
		printRules(stdout, rep)
	}
	return exitOK
}

// ruleFrom describes the source name: the project's stele.md, a shared file
// at the commit the index read it at, or an external source by its name.
func ruleFrom(name string, idx index.Index, pins map[string]source.Pin) ruleSource {
	if name == "stelemd" {
		return ruleSource{Name: name, Path: "stele.md"}
	}
	if p, ok := pins[name]; ok {
		return ruleSource{Name: name, Repo: p.Repo, Ref: p.Ref, Path: p.Path, Commit: idx.Sources[name]}
	}
	return ruleSource{Name: name}
}

// absent says which sources stele.lock holds rules of that the index does
// not: a source this machine has not compiled, such as shared rules after a
// fresh clone, or a pin a pull moved. Only stele compile reads them.
func absent(idx index.Index, committed lock.File) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range committed.Sources {
		seen[p.Source] = true
		if fp, ok := idx.Sources[p.Source]; !ok {
			out = append(out, fmt.Sprintf("%s: its rules are not in this machine's index: run stele compile", sourceLabel(p.Source)))
		} else if fp != p.Commit {
			out = append(out, fmt.Sprintf("%s: this machine's index holds it at %s, stele.lock pins %s: run stele compile", sourceLabel(p.Source), short(fp), short(p.Commit)))
		}
	}
	for _, e := range committed.Rules {
		if _, ok := idx.Sources[e.Source]; !ok && !seen[e.Source] {
			seen[e.Source] = true
			out = append(out, fmt.Sprintf("%s: its rules are not in this machine's index: run stele compile", sourceLabel(e.Source)))
		}
	}
	return out
}

// sourceLabel is a source as a person knows it: stele.md, a shared file by
// its host and path, an external source by the name stele.json gives it.
func sourceLabel(name string) string {
	if name == "stelemd" {
		return "stele.md"
	}
	return strings.TrimPrefix(name, "git:")
}

func printRulesJSON(w io.Writer, rep rulesReport) {
	data, _ := json.MarshalIndent(rep, "", "  ")
	w.Write(append(data, '\n'))
}

// printRules prints two lines a rule: its id, source and scope, then the
// start of its text.
func printRules(w io.Writer, rep rulesReport) {
	for _, r := range rep.Rules {
		from := sourceLabel(r.Source.Name)
		if r.Source.Commit != "" {
			from += "@" + short(r.Source.Commit)
		}
		scope := "every file"
		if len(r.Scope) > 0 {
			scope = strings.Join(r.Scope, ", ")
		}
		fmt.Fprintf(w, "%s  %s  %s\n  %s\n", r.ID, showPath(from), showPath(scope), showPath(check.Excerpt(r.Body)))
	}
	fmt.Fprintln(w, plural(len(rep.Rules), "rule"))
}
