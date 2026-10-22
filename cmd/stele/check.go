package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/source"
)

// runCheck validates the rules, then runs the checkers on the selected files,
// and writes nothing (spec §8). It exits 1 for an invalid rule or a blocking
// violation, and 2 when it could not run.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stele check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "check the files changed between `ref` and HEAD (in CI: origin/main)")
	all := fs.Bool("all", false, "check every tracked file")
	format := fs.String("format", "text", "output `format`: text or json")
	if err := fs.Parse(args); err != nil {
		return exitCantRun
	}
	paths := fs.Args()
	cantRun := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stele check: "+format+"\n", a...)
		return exitCantRun
	}
	switch {
	case *format != "text" && *format != "json":
		return cantRun("--format is text or json, not %q", *format)
	case *all && (*base != "" || len(paths) > 0):
		return cantRun("--all checks every tracked file: give it no --base and no paths")
	case *base != "" && len(paths) > 0:
		return cantRun("give --base or paths, not both")
	case strings.HasPrefix(*base, "-"):
		// git would read it as an option.
		return cantRun("--base %q is not a ref", *base)
	}
	root, err := findRoot(".")
	if err != nil {
		return cantRun("%v", err)
	}

	// 1. The rules.
	r, err := compile.Plan(context.Background(), root, sources(), uuid.NewString)
	if err != nil {
		var pe *source.ParseError
		if !errors.As(err, &pe) && !errors.Is(err, lock.ErrDuplicateID) && !errors.Is(err, lock.ErrInvalid) {
			return cantRun("%v", err)
		}
		return ruleFileProblem(root, *format, err.Error(), stdout, stderr)
	}
	// CI would otherwise report against ids that exist on nobody's machine.
	if r.LockChanged {
		return ruleFileProblem(root, *format, fmt.Sprintf("%s is out of date with the rules: run stele compile and commit %s",
			lock.FileName, lock.FileName), stdout, stderr)
	}

	// 2. The code.
	var files []string
	if len(paths) > 0 {
		files, err = explicitFiles(root, paths)
	} else {
		files, err = gitFiles(root, *base, *all)
	}
	if err != nil {
		return cantRun("%v", err)
	}
	files = ownedFiles(root, files)
	rep, err := check.Run(context.Background(), root, r.Index, files, plugin.BuiltinCheckers())
	if err != nil {
		return cantRun("%v", err)
	}

	if *format == "json" {
		printJSON(stdout, rep)
	} else {
		printCheck(stdout, r.Lock, rep, len(r.Index.Rules), len(files))
	}
	return rep.ExitCode()
}

// ruleFileProblem reports rule files stele check cannot use: on stderr as #7
// did, or in --format json as a problem with no rule id. The project is the
// committed lock's, when there is one.
func ruleFileProblem(root, format, msg string, stdout, stderr io.Writer) int {
	if format != "json" {
		fmt.Fprintf(stderr, "stele check: %s\n", msg)
		return exitFailed
	}
	committed, _ := lock.Load(root)
	rep := check.NewReport(committed.Project)
	rep.Problems = append(rep.Problems, check.Problem{Message: msg})
	printJSON(stdout, rep)
	return exitFailed
}

// ownedFiles drops stele's own files, and files that belong to a subproject
// with its own stele.md or stele.json: the hook gives those the subproject's
// rules (ADR-0004), so CI does too, from the subproject.
func ownedFiles(root string, files []string) []string {
	nested := map[string]bool{".": false}
	var inNested func(dir string) bool
	inNested = func(dir string) bool {
		if v, ok := nested[dir]; ok {
			return v
		}
		v := inNested(path.Dir(dir)) || usesStele(filepath.Join(root, filepath.FromSlash(dir)))
		nested[dir] = v
		return v
	}
	var out []string
	for _, f := range files {
		if !check.Own(f) && !inNested(path.Dir(f)) {
			out = append(out, f)
		}
	}
	return out
}

// explicitFiles turns paths given on the command line into paths relative to
// root. Each must be a file inside the project: a typo is an error, never a
// silent pass.
func explicitFiles(root string, paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the project at %s", p, root)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: no such file", p)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory: give files, or --all for every tracked file", p)
		}
		files = append(files, filepath.ToSlash(rel))
	}
	return dedupe(files), nil
}

// gitFiles asks git which files to check, relative to root: those changed
// against HEAD, staged, unstaged and untracked; those changed on this branch
// since it left base; or with all, every tracked file. Deleted files are
// left out. Only files under root are listed, so a stele.md in a subdirectory
// of the repository checks only its own files.
func gitFiles(root, base string, all bool) ([]string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("stele check needs git to find changed files; pass paths instead")
	}
	switch {
	case all:
		return git(root, "ls-files", "-z")
	case base != "":
		if _, err := git(root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
			return nil, fmt.Errorf("--base %s: unknown ref", base)
		}
		files, err := git(root, "diff", "--name-only", "-z", "--relative", "--diff-filter=ACMR", base+"...HEAD", "--")
		if err != nil && strings.Contains(err.Error(), "no merge base") {
			return nil, fmt.Errorf("%v (in CI, a shallow clone has no history: fetch with fetch-depth: 0)", err)
		}
		return files, err
	}
	if _, err := git(root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		// No commit yet, or not a repository: then ls-files says which.
		return git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	}
	changed, err := git(root, "diff", "--name-only", "-z", "--relative", "--diff-filter=ACMR", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	untracked, err := git(root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return dedupe(append(changed, untracked...)), nil
}

// git runs git in root and splits its NUL-separated output.
func git(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return dedupe(files), nil
}

func dedupe(files []string) []string {
	sort.Strings(files)
	out := files[:0]
	for i, f := range files {
		if i == 0 || f != files[i-1] {
			out = append(out, f)
		}
	}
	return out
}

// printCheck prints the report for a person: the rule problems, one line per
// violation, then a summary.
func printCheck(w io.Writer, lf lock.File, rep check.Report, rules, files int) {
	for _, p := range rep.Problems {
		fmt.Fprintf(w, "invalid rule %s \"%s\"%s: %s\n", p.RuleID, p.Excerpt, where(lf, p.RuleID), p.Message)
	}
	errs, warns := 0, 0
	for _, f := range rep.Violations {
		level := "error"
		if f.Blocking {
			errs++
		} else {
			level = "warning"
			warns++
		}
		fmt.Fprintf(w, "%s:%d:%d: %s %s \"%s\"", f.File, f.Line, f.Column, level, f.RuleID, f.Excerpt)
		if f.Message != f.Excerpt {
			fmt.Fprintf(w, ": %s", f.Message)
		}
		fmt.Fprintln(w)
	}

	if len(rep.Problems) == 0 {
		fmt.Fprintf(w, "%s valid\n", plural(rules, "rule"))
	} else {
		fmt.Fprintf(w, "%s in the rules\n", plural(len(rep.Problems), "problem"))
	}
	switch {
	case files == 0:
		fmt.Fprintln(w, "no files to check")
	case errs+warns == 0:
		fmt.Fprintf(w, "checked %s: no violations\n", plural(files, "file"))
	default:
		var counts []string
		if errs > 0 {
			counts = append(counts, plural(errs, "error"))
		}
		if warns > 0 {
			counts = append(counts, plural(warns, "warning"))
		}
		fmt.Fprintf(w, "checked %s: %s\n", plural(files, "file"), strings.Join(counts, ", "))
	}
}

// where says which rule in which source an id is, as printCompile does.
func where(lf lock.File, id string) string {
	for _, e := range lf.Rules {
		if e.ID == id {
			return fmt.Sprintf(" (%s rule %d)", e.Source, e.Position+1)
		}
	}
	return ""
}

func printJSON(w io.Writer, rep check.Report) {
	data, _ := json.MarshalIndent(rep, "", "  ")
	w.Write(append(data, '\n'))
}
