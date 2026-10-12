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
//   - a pattern ending in / matches everything under that directory: "docs/"
//   - a leading / or ./ anchors at the root, which every pattern is anyway
//
// A malformed pattern matches nothing.
package scope

import (
	"path"
	"strings"
)

// maxAlternatives caps brace expansion, so a pattern like {a,b}{c,d}... cannot
// make matching expensive.
const maxAlternatives = 256

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
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	for _, p := range expand(pattern) {
		if match(p, file) {
			return true
		}
	}
	return false
}

func match(pattern, file string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(file))
		return err == nil && ok
	}
	return segments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

// segments matches pattern segments against path segments, ** spanning any
// number of path segments.
func segments(pat, file []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for len(rest) > 0 && rest[0] == "**" {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return true
			}
			for i := range file {
				if segments(rest, file[i:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], file[0])
		if err != nil || !ok {
			return false
		}
		pat, file = pat[1:], file[1:]
	}
	return len(file) == 0
}

// expand returns the pattern with every {a,b} group expanded, innermost
// alternatives included. An unbalanced brace leaves the pattern as it is, and
// path.Match then treats the brace literally.
func expand(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	depth, close := 0, -1
	var commas []int
	for i := open; i < len(pattern) && close < 0; i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
			}
		case ',':
			if depth == 1 {
				commas = append(commas, i)
			}
		}
	}
	if close < 0 {
		return []string{pattern}
	}
	prefix, suffix := pattern[:open], pattern[close+1:]
	var alts []string
	start := open + 1
	for _, c := range append(commas, close) {
		alts = append(alts, pattern[start:c])
		start = c + 1
	}
	var out []string
	for _, a := range alts {
		for _, e := range expand(prefix + a + suffix) {
			if len(out) == maxAlternatives {
				return out
			}
			out = append(out, e)
		}
	}
	return out
}
