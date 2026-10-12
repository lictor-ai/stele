// Package scope matches a rule's scope globs against a file path.
//
// In v1 a rule's scope decides which files the hook shows it for (D58), and
// which files a checker runs it on (spec §8). A path is relative to the
// project root and uses / on every platform. A pattern is matched like this:
//
//   - **  matches any number of directories, including none: "**/*.go"
//   - *, ?, [a-z]  match within one path segment, as path.Match does
//   - {a,b}  matches either alternative: "src/**/*.{go,ts}"
//   - a pattern with no / matches the file's name at any depth: "*.go"
//   - a leading / or ./ anchors it at the root: "/*.go" is root files only
//   - a pattern ending in / matches everything under that directory: "docs/"
//
// On Windows and macOS, whose file systems ignore case, so does matching: an
// agent that writes Internal/x.go writes into internal/.
//
// A malformed pattern, one longer than 1 KB, or one that expands to more than
// 256 alternatives matches nothing. Those caps bound the work whatever the
// pattern: stele.md comes from the repository, and the hook runs on every
// write. Real scope globs are a few dozen characters.
package scope

import (
	"path"
	"runtime"
	"strings"
)

const (
	// maxPattern caps a pattern's length. Brace expansion and matching are
	// bounded by it, times maxAlternatives.
	maxPattern = 1024
	// maxAlternatives caps brace expansion.
	maxAlternatives = 256
)

// foldCase is whether matching ignores case, as the platform's file system
// does. Tests set it.
var foldCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// Applies reports whether a rule with these scope patterns applies to file. A
// rule with no scope applies everywhere.
func Applies(patterns []string, file string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if Match(p, file) {
			return true
		}
	}
	return false
}

// Match reports whether file matches the glob pattern.
func Match(pattern, file string) bool {
	if len(pattern) > maxPattern {
		return false
	}
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	if foldCase {
		pattern, file = strings.ToLower(pattern), strings.ToLower(file)
	}
	alts, ok := expand(pattern)
	if !ok {
		return false
	}
	for _, p := range alts {
		if match(p, file) {
			return true
		}
	}
	return false
}

func match(pattern, file string) bool {
	anchored := strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "./")
	pattern = strings.TrimPrefix(strings.TrimPrefix(pattern, "./"), "/")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if pattern == "" {
		return false
	}
	if !anchored && !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(file))
		return err == nil && ok
	}
	return segments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

// segments matches pattern segments against path segments, ** spanning any
// number of path segments. Each (pattern, path) position is computed once,
// so many ** cannot make it backtrack exponentially.
func segments(pat, file []string) bool {
	n, m := len(pat), len(file)
	const (
		unknown = iota
		yes
		no
	)
	memo := make([]uint8, (n+1)*(m+1))
	var at func(i, j int) bool
	at = func(i, j int) bool {
		k := i*(m+1) + j
		if memo[k] != unknown {
			return memo[k] == yes
		}
		var r bool
		switch {
		case i == n:
			r = j == m
		case pat[i] == "**":
			r = at(i+1, j) || (j < m && at(i, j+1))
		case j == m:
			r = false
		default:
			ok, err := path.Match(pat[i], file[j])
			r = err == nil && ok && at(i+1, j+1)
		}
		memo[k] = no
		if r {
			memo[k] = yes
		}
		return r
	}
	return at(0, 0)
}

// expand returns the pattern with every {a,b} group expanded, nested groups
// included, or false when that would make more than maxAlternatives. It
// builds left to right and stops as soon as the cap is passed, so the work is
// bounded too, not only the result. An unbalanced brace is left as it is.
func expand(pattern string) ([]string, bool) {
	out := []string{""}
	for pattern != "" {
		open := strings.IndexByte(pattern, '{')
		close, commas := group(pattern, open)
		if open < 0 || close < 0 {
			for i := range out {
				out[i] += pattern
			}
			return out, true
		}
		literal := pattern[:open]
		var alts []string
		start := open + 1
		for _, c := range append(commas, close) {
			sub, ok := expand(pattern[start:c])
			if !ok {
				return nil, false
			}
			alts = append(alts, sub...)
			start = c + 1
		}
		if len(out)*len(alts) > maxAlternatives {
			return nil, false
		}
		next := make([]string, 0, len(out)*len(alts))
		for _, prefix := range out {
			for _, a := range alts {
				next = append(next, prefix+literal+a)
			}
		}
		out, pattern = next, pattern[close+1:]
	}
	return out, true
}

// group finds the } closing the { at open, and the commas at its own depth.
// close is -1 when there is no group or it never closes.
func group(pattern string, open int) (close int, commas []int) {
	if open < 0 {
		return -1, nil
	}
	depth := 0
	for i := open; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, commas
			}
		case ',':
			if depth == 1 {
				commas = append(commas, i)
			}
		}
	}
	return -1, nil
}
