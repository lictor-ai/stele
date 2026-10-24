// Package stelemd is the built-in stele.md source: read, hash, parse.
//
// stele.md sits at the repository root. Rules are separated by a line holding
// only ---, except inside a fenced code block. Inside a rule, leading key: value lines are metadata, and
// everything after the first blank line is the body (spec §3).
//
// A key is lowercase letters, digits, _ and -, starting with a letter, and is
// followed by a colon and then whitespace or the end of the line. So a body
// opening with "Note: ..." or a URL is still body. A value is the rest of the
// line, trimmed, with one pair of matching outer quotes removed.
//
// HTML comments before a rule's metadata are skipped, so the header comment
// stele init writes is not a rule. As in a code block, a --- inside a comment
// does not separate rules.
package stelemd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/source"
)

const (
	name     = "stelemd"
	fileName = "stele.md"
)

// metaLine is a key: value line. The value group is empty for "key:".
var metaLine = regexp.MustCompile(`^([a-z][a-z0-9_-]*):(?:[ \t]+(.*))?$`)

type stelemd struct{}

// New returns the stele.md source.
func New() source.Source { return stelemd{} }

func (stelemd) Name() string { return name }

// Read parses stele.md under root. No stele.md is no rules and an empty
// fingerprint, not an error: other sources may supply the rules.
func (stelemd) Read(ctx context.Context, root string) ([]source.Rule, string, error) {
	src, err := readFile(ctx, root)
	if err != nil || src == nil {
		return nil, "", err
	}
	rules, err := Parse(src)
	if err != nil {
		return nil, "", err
	}
	return rules, fingerprint(src), nil
}

// Fingerprint hashes the raw bytes of stele.md, or returns "" when it is absent.
func (stelemd) Fingerprint(ctx context.Context, root string) (string, error) {
	src, err := readFile(ctx, root)
	if err != nil || src == nil {
		return "", err
	}
	return fingerprint(src), nil
}

// maxSize caps stele.md. It is read on every write, by the hook.
const maxSize = 4 << 20

// readFile returns nil, nil when stele.md does not exist.
//
// A symlink is followed, since a user may link stele.md on purpose, but it
// must end at a regular file of bounded size: a repository could commit
// stele.md as a link to /dev/zero, and the hook reads it on every write.
func readFile(ctx context.Context, root string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, fileName)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", fileName)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(src) > maxSize {
		return nil, fmt.Errorf("%s is over %d bytes", fileName, maxSize)
	}
	return src, nil
}

func fingerprint(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// Parse turns the contents of stele.md into rules. A bad rule is a
// *source.ParseError naming the line.
func Parse(src []byte) ([]source.Rule, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	var rules []source.Rule
	start := 0
	fence, fenceLine := "", 0 // the open code fence, if any, and its index
	comment, commentLine := false, 0
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) {
			if comment {
				if strings.Contains(lines[i], "-->") {
					comment = false
				}
				continue
			}
			if fence != "" {
				if closesFence(lines[i], fence) {
					fence = ""
				}
				continue
			}
			if opensComment(lines[i]) {
				comment, commentLine = true, i
				continue
			}
			if f := opensFence(lines[i]); f != "" {
				fence, fenceLine = f, i
				continue
			}
			if strings.TrimRight(lines[i], " \t") != "---" {
				continue
			}
		} else if fence != "" {
			return nil, &source.ParseError{File: fileName, Line: fenceLine + 1, Err: errors.New("unclosed code block")}
		} else if comment {
			return nil, &source.ParseError{File: fileName, Line: commentLine + 1, Err: errors.New("unclosed comment")}
		}
		r, ok, err := parseRule(lines[start:i], start+1)
		if err != nil {
			return nil, err
		}
		if ok {
			r.Position = len(rules)
			rules = append(rules, r)
		}
		start = i + 1
	}
	return rules, nil
}

// opensFence returns the fence a line opens, such as ``` or ~~~~, or "". As in
// CommonMark, a fence is three or more backticks or tildes, indented at most
// three spaces.
func opensFence(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return ""
	}
	n := len(trimmed) - len(strings.TrimLeft(trimmed, string(c)))
	if n < 3 {
		return ""
	}
	// A backtick fence's info string cannot hold a backtick: ```x``` is
	// inline code, not a fence.
	if c == '`' && strings.Contains(trimmed[n:], "`") {
		return ""
	}
	return trimmed[:n]
}

// opensComment reports whether line opens an HTML comment that does not close
// on the same line. Until it closes, no line separates rules or opens a fence.
func opensComment(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "<!--")
	return ok && !strings.Contains(rest, "-->")
}

// closesFence reports whether line closes fence: the same character, at least
// as many of it, and nothing else on the line.
func closesFence(line, fence string) bool {
	f := opensFence(line)
	return f != "" && f[0] == fence[0] && len(f) >= len(fence) &&
		strings.TrimSpace(strings.TrimLeft(line, " ")[len(f):]) == ""
}

// parseRule parses the lines between two separators. first is the 1-based
// line number of lines[0]. ok is false for a block with no rule in it.
func parseRule(lines []string, first int) (r source.Rule, ok bool, err error) {
	fail := func(i int, format string, args ...any) (source.Rule, bool, error) {
		return source.Rule{}, false, &source.ParseError{File: fileName, Line: first + i, Err: fmt.Errorf(format, args...)}
	}

	lines, i, unclosed := skipPreamble(lines)
	if unclosed >= 0 {
		return fail(unclosed, "unclosed comment")
	}
	if i == len(lines) {
		return source.Rule{}, false, nil
	}

	r = source.Rule{Source: name, File: fileName, Line: first + i}
	seen := map[string]bool{}
	start := i
	for ; i < len(lines); i++ {
		m := metaLine.FindStringSubmatch(strings.TrimRight(lines[i], " \t"))
		if m == nil {
			break
		}
		key, value := m[1], m[2]
		if seen[key] {
			return fail(i, "duplicate key %q", key)
		}
		seen[key] = true
		switch key {
		case "description":
			r.Description = unquote(value)
		case "tags", "scope":
			list, err := parseList(value)
			if err != nil {
				return fail(i, "%s: %v", key, err)
			}
			if key == "tags" {
				r.Tags = list
			} else {
				if err := scope.Validate(list); err != nil {
					return fail(i, "%s: %v", key, err)
				}
				r.Scope = list
			}
		default:
			if r.Meta == nil {
				r.Meta = map[string]string{}
			}
			r.Meta[key] = unquote(value)
		}
	}
	if i > start && i < len(lines) && strings.TrimSpace(lines[i]) != "" {
		return fail(i, "metadata must be followed by a blank line")
	}

	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	r.Body = strings.TrimRight(strings.Join(lines[i:], "\n"), " \t\n")
	if r.Body == "" {
		return fail(start, "rule has no body")
	}
	return r, true, nil
}

// skipPreamble skips blank lines and HTML comments, and returns the index of
// the first line of the rule. unclosed is the index of a comment that never
// closes, or -1. Text after a comment's --> is the start of the rule, so the
// returned lines may hold that line with the comment cut off.
func skipPreamble(lines []string) (out []string, i, unclosed int) {
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			i++
		case strings.HasPrefix(line, "<!--"):
			end := i
			for end < len(lines) && !strings.Contains(lines[end], "-->") {
				end++
			}
			if end == len(lines) {
				return lines, i, i
			}
			_, rest, _ := strings.Cut(lines[end], "-->")
			if rest = strings.TrimLeft(rest, " \t"); strings.TrimSpace(rest) == "" {
				i = end + 1
				continue
			}
			lines = append([]string(nil), lines...)
			lines[end] = rest
			i = end
		default:
			return lines, i, -1
		}
	}
	return lines, i, -1
}

// unquote trims s and removes one pair of matching outer quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// parseList reads "[a, b]" as a list and anything else as a list of one.
// Commas inside quotes or braces do not split, so "*.{go,ts}" is one glob.
func parseList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		if v := unquote(s); v != "" {
			return []string{v}, nil
		}
		return nil, nil
	}
	if !strings.HasSuffix(s, "]") {
		return nil, errors.New("unclosed list")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, nil
	}

	var items []string
	var quote byte
	depth, from := 0, 0
	add := func(item string) error {
		v := unquote(item)
		if v == "" {
			return errors.New("empty list item")
		}
		items = append(items, v)
		return nil
	}
	for j := 0; j < len(inner); j++ {
		c := inner[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && strings.TrimSpace(inner[from:j]) == "":
			// Only a quote that opens an item quotes it: don't-care is a
			// plain item.
			quote = c
		case c == '{':
			depth++
		case c == '}' && depth > 0:
			depth--
		case c == ',' && depth == 0:
			if err := add(inner[from:j]); err != nil {
				return nil, err
			}
			from = j + 1
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if err := add(inner[from:]); err != nil {
		return nil, err
	}
	return items, nil
}
