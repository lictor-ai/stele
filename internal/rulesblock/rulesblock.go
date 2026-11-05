// Package rulesblock reads the rule blocks of a rules file stele writes for
// an agent to load (#68): each rule opens with a marker line naming its id,
// and its block is the lines up to the next marker. Every adapter's rules
// file uses the same blocks and the same hash, so a session's record of what
// it loaded reads the same whichever agent wrote it.
package rulesblock

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// marker opens a rule's block. The id's characters are those of a lock id; a
// rule whose id has others is left out of the file.
var marker = regexp.MustCompile(`^<!-- stele rule ([A-Za-z0-9._:-]{1,128}) -->$`)

// Marker is the line that opens id's block.
func Marker(id string) string { return "<!-- stele rule " + id + " -->" }

// ValidID reports whether id can be a marker's id.
func ValidID(id string) bool { return marker.MatchString(Marker(id)) }

// IsMarker reports whether line, trailing spaces aside, opens a block.
func IsMarker(line string) bool { return marker.MatchString(strings.TrimRight(line, " \t\r")) }

// Block is one rule's block, in file order.
type Block struct{ ID, Hash string }

// Blocks splits a text into its rule blocks, duplicates included.
func Blocks(data []byte) []Block {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []Block
	id, start := "", -1
	flush := func(end int) {
		if id != "" {
			out = append(out, Block{id, hash(id, lines[start:end])})
		}
	}
	for i, line := range lines {
		m := marker.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
		if m == nil {
			continue
		}
		flush(i)
		id, start = m[1], i+1
	}
	flush(len(lines))
	return out
}

// Read returns the rules in a text as the agent read it: rule id to a hash
// of its block's text. It is the only code that hashes a block, so the file's
// side and an adapter's RuleHash agree by construction. Line ends, a byte
// order mark, trailing spaces and blank lines between blocks do not count, as
// a checkout may change them; any other change to a block's text, its scope
// line included, does. An id found twice is dropped, every copy.
func Read(data []byte) map[string]string {
	got := map[string]string{}
	seen := map[string]bool{}
	for _, b := range Blocks(data) {
		if seen[b.ID] {
			delete(got, b.ID)
			continue
		}
		seen[b.ID] = true
		got[b.ID] = b.Hash
	}
	return got
}

// Same reports whether two texts hold the same blocks in the same order.
func Same(a, b []byte) bool {
	x, y := Blocks(a), Blocks(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// hash hashes a block's lines, trimmed at the right, without the blank lines
// around them. The version prefix lets a later format prime nothing, which is
// safe, rather than prime the wrong text.
func hash(id string, lines []string) string {
	trimmed := make([]string, len(lines))
	for i, l := range lines {
		trimmed[i] = strings.TrimRight(l, " \t\r")
	}
	for len(trimmed) > 0 && trimmed[0] == "" {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	sum := sha256.Sum256([]byte("stele-rule-v1\n" + id + "\n" + strings.Join(trimmed, "\n")))
	return hex.EncodeToString(sum[:16])
}
