package gitexec

import "github.com/lictor-ai/stele/internal/printable"

// Clean makes s one printable line (printable.Clean): what git, a host or
// stele.json supplies can then neither rewrite the terminal, nor add a line
// that looks like stele's own, nor disguise a link.
func Clean(s string) string { return printable.Clean(s) }

// CleanLines cleans each line of s with Clean and keeps the line breaks: for
// text meant to span lines, such as git's message when it fails.
func CleanLines(s string) string { return printable.Lines(s) }
