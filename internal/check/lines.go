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
// the lines an edit wrote, found from its new text when the agent gave no
// patch. Line ends at either end of a fragment do not count, so "\nx" covers
// only the line of x. In a CRLF file, a fragment with LF line ends is looked
// for with CRLF ones too, as an agent keeps a file's line ends.
//
// A fragment without ReplaceAll was written once. When it occurs more than
// once, which occurrence is the edit cannot be told, and it covers no lines:
// stele check still sees the file. With ReplaceAll, the first maxOccurrences
// count.
func Spans(content []byte, fragments []Fragment) []Span {
	spans, _ := spansRead(content, fragments)
	return spans
}

// spansRead is Spans, and how many bytes of content it read: the file once to
// tell its line ends, each fragment searched for once, or twice in a CRLF
// file, and the lines numbered in one pass. That is at most
// (2·len(fragments)+2)·len(content), however many occurrences there are.
func spansRead(content []byte, fragments []Fragment) (_ []Span, read int) {
	type occurrence struct{ first, last int } // byte offsets, inclusive
	var occs []occurrence
	crlf := bytes.Contains(content, []byte("\r\n"))
	read += len(content)
	for _, f := range fragments {
		text := f.Text
		found, n := find(content, text, f.ReplaceAll)
		read += n
		if len(found) == 0 && crlf && strings.Contains(text, "\n") && !strings.Contains(text, "\r\n") {
			text = strings.ReplaceAll(text, "\n", "\r\n")
			found, n = find(content, text, f.ReplaceAll)
			read += n
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
		return nil, read
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
		read += off - pos
		pos = off
		lineAt[off] = line
	}
	spans := make([]Span, len(occs))
	for i, o := range occs {
		spans[i] = Span{lineAt[o.first], lineAt[o.last]}
	}
	return Merge(spans), read
}

// find returns the start of each non-overlapping occurrence of text, at most
// maxOccurrences, and how many bytes of content it read: each search starts
// where the last match ended. Without all, it returns nil unless there is
// exactly one.
func find(content []byte, text string, all bool) (starts []int, read int) {
	if text == "" {
		return nil, 0
	}
	frag := []byte(text)
	for off := 0; len(starts) < maxOccurrences; {
		i := bytes.Index(content[off:], frag)
		if i < 0 {
			read += len(content) - off
			break
		}
		read += i + len(frag)
		starts = append(starts, off+i)
		if !all && len(starts) > 1 {
			return nil, read // ambiguous
		}
		off += i + len(frag)
	}
	return starts, read
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
	in, _ := containProbes(spans, line)
	return in
}

// containProbes is Contain, and how many spans it looked at: a binary search
// looks at no more than bits.Len(len(spans)) of them.
func containProbes(spans []Span, line int) (in bool, probes int) {
	i := sort.Search(len(spans), func(i int) bool { probes++; return spans[i].Last >= line })
	return i < len(spans) && spans[i].First <= line, probes
}
