package gitexec

import (
	"regexp"
	"strings"
	"unicode"
)

// csi is a terminal escape sequence: ESC [ parameters, a final letter.
var csi = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Clean makes s one printable line: it removes terminal escape sequences,
// control characters and the characters that reorder text, and turns a
// newline or a tab into a space. What git, a host or stele.json supplies can
// then neither rewrite the terminal, nor add a line that looks like stele's
// own, nor disguise a link.
func Clean(s string) string {
	s = csi.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return ' '
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F:
			return -1
		}
		return r
	}, s)
}

// CleanLines cleans each line of s with Clean and keeps the line breaks: for
// text meant to span lines, such as git's message when it fails.
func CleanLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Clean(l)
	}
	return strings.Join(lines, "\n")
}
