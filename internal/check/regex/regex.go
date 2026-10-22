// Package regex is the built-in regex checker.
//
// A rule with `check: regex` names a Go regular expression (RE2) in its
// `pattern` key. Each match is a violation, reported where the match starts.
// The pattern runs over the whole file, so ^ and $ match per line only with
// (?m). An optional `message` key replaces the default message, the rule's
// excerpt.
package regex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/scope"
)

// Name is the checker's name, as a rule's `check` key says it.
const Name = "regex"

// binaryPrefix is how much of a file is looked at for a NUL byte. A file with
// one there is binary and skipped.
const binaryPrefix = 8 << 10

// maxFile caps the files Check reads; larger ones are skipped. Tests lower it.
var maxFile int64 = 32 << 20

// Location is where a match starts: 1-based, with the column in bytes.
type Location struct{ Line, Column int }

// Match returns where each match of re in content starts.
func Match(re *regexp.Regexp, content []byte) []Location {
	var locs []Location
	line, lineStart, scanned := 1, 0, 0
	for _, m := range re.FindAllIndex(content, -1) {
		for ; scanned < m[0]; scanned++ {
			if content[scanned] == '\n' {
				line++
				lineStart = scanned + 1
			}
		}
		locs = append(locs, Location{Line: line, Column: m[0] - lineStart + 1})
	}
	return locs
}

// Checker is the regex checker.
type Checker struct{}

// New returns the regex checker.
func New() Checker { return Checker{} }

// Name is "regex".
func (Checker) Name() string { return Name }

// Validate reports a rule with no pattern, one that does not compile, and one
// that matches the empty string: that would be a violation at every position
// of every file.
func (Checker) Validate(rules []check.Rule) []check.Problem {
	var ps []check.Problem
	for _, r := range rules {
		if _, err := compile(r); err != nil {
			ps = append(ps, check.Problem{RuleID: r.ID, Message: err.Error()})
		}
	}
	return ps
}

// Check reads each file once and runs every rule that applies to it. A file
// that no longer exists, is not a regular file or is too large is skipped.
func (c Checker) Check(ctx context.Context, root string, rules []check.Rule, files []string) ([]check.Violation, error) {
	compiled := compileAll(rules)
	var vs []check.Violation
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := safefile.ReadRegular(filepath.Join(root, filepath.FromSlash(f)), maxFile)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, safefile.ErrNotRegular) || errors.Is(err, safefile.ErrTooLarge) {
			continue
		}
		if err != nil {
			return nil, err
		}
		vs = append(vs, checkContent(compiled, f, content)...)
	}
	return vs, nil
}

// CheckContent runs every rule that applies to file on content. A binary file
// gives nothing.
func (Checker) CheckContent(rules []check.Rule, file string, content []byte) []check.Violation {
	return checkContent(compileAll(rules), file, content)
}

type compiled struct {
	rule check.Rule
	re   *regexp.Regexp
}

// compileAll compiles each rule's pattern. A rule whose pattern does not
// compile is left out; Validate has already reported it.
func compileAll(rules []check.Rule) []compiled {
	var cs []compiled
	for _, r := range rules {
		if re, err := compile(r); err == nil {
			cs = append(cs, compiled{r, re})
		}
	}
	return cs
}

func compile(r check.Rule) (*regexp.Regexp, error) {
	p := r.Meta["pattern"]
	if p == "" {
		return nil, errors.New("regex: no pattern")
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("regex: pattern does not compile: %v", err)
	}
	if re.Match(nil) {
		return nil, fmt.Errorf("regex: pattern %q matches empty text, so it would match everywhere", p)
	}
	return re, nil
}

func checkContent(cs []compiled, file string, content []byte) []check.Violation {
	if bytes.IndexByte(content[:min(len(content), binaryPrefix)], 0) >= 0 {
		return nil
	}
	var vs []check.Violation
	for _, c := range cs {
		if !scope.Applies(c.rule.Scope, file) {
			continue
		}
		for _, l := range Match(c.re, content) {
			vs = append(vs, check.Violation{RuleID: c.rule.ID, File: file, Line: l.Line, Column: l.Column, Message: c.rule.Meta["message"]})
		}
	}
	return vs
}
