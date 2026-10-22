package check

import (
	"bytes"
	"sort"
	"strings"
)

// Span is lines First to Last, 1-based and inclusive.
type Span struct{ First, Last int }

// Spans returns the lines covered by each occurrence of each non-empty
// fragment in content, sorted: the lines an Edit wrote, from its new_string.
// A fragment ending in a newline does not take in the next line. In a CRLF
// file, a fragment with LF line ends is looked for with CRLF ones too, as
// Claude Code keeps a file's line ends.
func Spans(content []byte, fragments []string) []Span {
	var spans []Span
	crlf := bytes.Contains(content, []byte("\r\n"))
	for _, f := range fragments {
		if f == "" {
			continue
		}
		found := occurrences(content, []byte(f))
		if len(found) == 0 && crlf && strings.Contains(f, "\n") && !strings.Contains(f, "\r\n") {
			found = occurrences(content, []byte(strings.ReplaceAll(f, "\n", "\r\n")))
		}
		spans = append(spans, found...)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].First < spans[j].First })
	return spans
}

// occurrences returns the span of each non-overlapping occurrence of frag.
func occurrences(content, frag []byte) []Span {
	var spans []Span
	for off := 0; ; {
		i := bytes.Index(content[off:], frag)
		if i < 0 {
			return spans
		}
		start := off + i
		end := start + len(frag) - 1 // the fragment's last byte
		first := 1 + bytes.Count(content[:start], []byte("\n"))
		last := first + bytes.Count(content[start:end], []byte("\n"))
		spans = append(spans, Span{first, last})
		off = start + len(frag)
	}
}

// Contain reports whether line is in one of spans.
func Contain(spans []Span, line int) bool {
	for _, s := range spans {
		if s.First <= line && line <= s.Last {
			return true
		}
	}
	return false
}
