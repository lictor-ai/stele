package source

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Hash is the normalised hash of a rule body. Normalising is lowercase, runs of
// whitespace collapsed, leading and trailing whitespace and punctuation
// stripped per line, and lines joined with a single space, so a rewrap or a
// trailing full stop does not change it.
//
// It is for reconciliation, staleness and drift, never identity (D20). It
// lives here so every source hashes alike and a rule moving between sources
// keeps its id.
func Hash(body string) string {
	var lines []string
	for line := range strings.SplitSeq(strings.ToLower(body), "\n") {
		line = strings.TrimFunc(line, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsPunct(r)
		})
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, " ")))
	return hex.EncodeToString(sum[:])
}
