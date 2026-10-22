package check

import (
	"bytes"
	"sort"
	"strings"
)

// Span is lines First to Last, 1-based and inclusive.
type Span struct{ First, Last int }

// Fragment is the new text of one edit, and whether it replaced every
// occurrence.
type Fragment struct {
	Text       string
	ReplaceAll bool
}

// maxOccurrences caps the occurrences of one fragment Spans looks for.
const maxOccurrences = 1000

// Spans returns the lines each fragment covers in content, sorted and merged:
// the lines an Edit wrote, found from its new_string when Claude Code gave no
// patch. Line ends at either end of a fragment do not count, so "\nx" covers
// only the line of x. In a CRLF file, a fragment with LF line ends is looked
// for with CRLF ones too, as Claude Code keeps a file's line ends.
//
// A fragment without ReplaceAll was written once. When it occurs more than
// once, which occurrence is the edit cannot be told, and it covers no lines:
// stele check still sees the file. With ReplaceAll, the first maxOccurrences
// count.
func Spans(content []byte, fragments []Fragment) []Span {
	type occurrence struct{ first, last int } // byte offsets, inclusive
	var occs []occurrence
	crlf := bytes.Contains(content, []byte("\r\n"))
	for _, f := range fragments {
		text := f.Text
		found := find(content, text, f.ReplaceAll)
		if len(found) == 0 && crlf && strings.Contains(text, "\n") && !strings.Contains(text, "\r\n") {
			text = strings.ReplaceAll(text, "\n", "\r\n")
			found = find(content, text, f.ReplaceAll)
		}
		lead := len(text) - len(strings.TrimLeft(text, "\r\n"))
		trail := len(text) - len(strings.TrimRight(text, "\r\n"))
		if lead+trail >= len(text) {
			continue // only line ends: no line of its own
		}
		for _, start := range found {
			occs = append(occs, occurrence{start + lead, start + len(text) - trail - 1})
		}
	}
	if len(occs) == 0 {
		return nil
	}

	// Number the lines in one pass over the offsets, in order.
	offsets := make([]int, 0, 2*len(occs))
	for _, o := range occs {
		offsets = append(offsets, o.first, o.last)
	}
	sort.Ints(offsets)
	lineAt := make(map[int]int, len(offsets))
	line, pos := 1, 0
	for _, off := range offsets {
		line += bytes.Count(content[pos:off], []byte("\n"))
		pos = off
		lineAt[off] = line
	}
	spans := make([]Span, len(occs))
	for i, o := range occs {
		spans[i] = Span{lineAt[o.first], lineAt[o.last]}
	}
	return Merge(spans)
}

// find returns the start of each non-overlapping occurrence of text, at most
// maxOccurrences. Without all, it returns nil unless there is exactly one.
func find(content []byte, text string, all bool) []int {
	if text == "" {
		return nil
	}
	frag := []byte(text)
	var starts []int
	for off := 0; len(starts) < maxOccurrences; {
		i := bytes.Index(content[off:], frag)
		if i < 0 {
			break
		}
		starts = append(starts, off+i)
		if !all && len(starts) > 1 {
			return nil // ambiguous
		}
		off += i + len(frag)
	}
	return starts
}

// Merge sorts spans and joins those that overlap or touch, as Contain needs.
func Merge(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].First < spans[j].First })
	merged := spans[:1]
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if s.First <= last.Last+1 {
			last.Last = max(last.Last, s.Last)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// Contain reports whether line is in one of spans, which are merged.
func Contain(spans []Span, line int) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].Last >= line })
	return i < len(spans) && spans[i].First <= line
}
