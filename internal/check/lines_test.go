package check

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
)

func frags(texts ...string) []Fragment {
	fs := make([]Fragment, len(texts))
	for i, t := range texts {
		fs[i] = Fragment{Text: t}
	}
	return fs
}

func TestSpans(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		fragments []Fragment
		want      []Span
	}{
		{name: "one line", content: "a\nb\nc\n", fragments: frags("b"), want: []Span{{2, 2}}},
		{name: "several lines", content: "a\nb\nc\nd\n", fragments: frags("b\nc"), want: []Span{{2, 3}}},
		{name: "a fragment ending in a newline stops at its line", content: "a\nb\nc\n", fragments: frags("b\n"), want: []Span{{2, 2}}},
		{name: "a fragment starting with a newline starts at the next line", content: "a\nb\nc\n", fragments: frags("\nb"), want: []Span{{2, 2}}},
		{name: "a fragment starting with CRLF", content: "a\r\nb\r\nc\r\n", fragments: frags("\r\nb"), want: []Span{{2, 2}}},
		{name: "only newlines", content: "a\n\n\nb\n", fragments: frags("\n\n"), want: nil},
		{name: "every occurrence with replace_all", content: "x\ny\nx\n", fragments: []Fragment{{Text: "x", ReplaceAll: true}}, want: []Span{{1, 1}, {3, 3}}},
		// Without replace_all only one occurrence was written, and which one
		// cannot be told: none is attributed. stele check covers the file.
		{name: "ambiguous without replace_all", content: "x\ny\nx\n", fragments: frags("x"), want: nil},
		{name: "not found", content: "a\nb\n", fragments: frags("z"), want: nil},
		{name: "empty fragment", content: "a\nb\n", fragments: frags(""), want: nil},
		{name: "at the start and the end, no final newline", content: "start\nmid\nend", fragments: frags("start", "end"), want: []Span{{1, 1}, {3, 3}}},
		{name: "a CRLF file and an LF fragment", content: "a\r\nb\r\nc\r\n", fragments: frags("b\nc"), want: []Span{{2, 3}}},
		{name: "several fragments, sorted", content: "a\nb\nc\nd\n", fragments: frags("d", "b"), want: []Span{{2, 2}, {4, 4}}},
		{name: "overlapping and adjacent spans merge", content: "a\nb\nc\nd\ne\n", fragments: frags("b\nc", "c\nd", "e"), want: []Span{{2, 5}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Spans([]byte(tt.content), tt.fragments)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Spans (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSpansCapsOccurrences(t *testing.T) {
	content := []byte(strings.Repeat("x\ny\n", 1500))
	got := Spans(content, []Fragment{{Text: "x", ReplaceAll: true}})
	if len(got) != maxOccurrences || got[len(got)-1].Last != 2*maxOccurrences-1 {
		t.Errorf("want the first %d occurrences, got %d spans ending %v", maxOccurrences, len(got), got[len(got)-1])
	}
}

func TestContain(t *testing.T) {
	s := Merge([]Span{{7, 9}, {2, 3}, {3, 4}})
	if diff := cmp.Diff([]Span{{2, 4}, {7, 9}}, s); diff != "" {
		t.Errorf("Merge (-want +got):\n%s", diff)
	}
	for line, want := range map[int]bool{1: false, 2: true, 4: true, 5: false, 7: true, 9: true, 10: false} {
		if got := Contain(s, line); got != want {
			t.Errorf("Contain(%d) = %v, want %v", line, got, want)
		}
	}
}

// Spans and Contain stay linear, or close: a large file with many edits
// must not slow every write.
func TestSpansLargeFile(t *testing.T) {
	start := time.Now()
	var b strings.Builder
	for b.Len() < 1<<20 {
		b.WriteString("MARK x := 1\n")
		for range 100 {
			b.WriteString("y\n")
		}
	}
	got := Spans([]byte(b.String()), []Fragment{{Text: "MARK", ReplaceAll: true}, {Text: "\ny\ny\n", ReplaceAll: true}})
	if len(got) == 0 {
		t.Fatal("no spans")
	}

	many := make([]Span, 100_000)
	for i := range many {
		many[i] = Span{3*i + 1, 3*i + 2}
	}
	many = Merge(many)
	n := 0
	for line := 1; line <= 300_000; line++ {
		if Contain(many, line) {
			n++
		}
	}
	if n != 200_000 {
		t.Errorf("Contain found %d lines, want 200000", n)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v, want well under a second", d)
	}
}

// The filter runs before the cap per rule and file, so the agent's violation
// is kept even in a file that already breaks the rule 150 times.
func TestRunFileLines(t *testing.T) {
	idx := index.Index{Project: "p", Rules: []index.Entry{entry("a", "Rule A.", nil, map[string]string{"check": "fake", "blocking": "false"})}}
	var found []Violation
	for i := 1; i <= 150; i++ {
		found = append(found, Violation{RuleID: "a", File: "f.go", Line: i, Column: 1})
	}
	found = append(found, Violation{RuleID: "a", File: "f.go", Line: 200, Column: 3})
	f := &contentFake{fake: fake{name: "fake", found: found}}
	cs := map[string]Checker{"fake": f}

	r := RunFileLines(idx, "f.go", []byte("x"), cs, func(line int) bool { return line == 200 })
	want := []Finding{{RuleID: "a", Excerpt: "Rule A.", File: "f.go", Line: 200, Column: 3, Message: "Rule A.", Blocking: false}}
	if diff := cmp.Diff(want, r.Violations); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
	if r.Truncated != 0 {
		t.Errorf("Truncated = %d, want 0", r.Truncated)
	}

	all := RunFileLines(idx, "f.go", []byte("x"), cs, nil)
	if diff := cmp.Diff(RunFile(idx, "f.go", []byte("x"), cs), all); diff != "" {
		t.Errorf("a nil keep differs from RunFile (-RunFile +got):\n%s", diff)
	}
	if got := len(all.Violations) + all.Truncated; got != 151 {
		t.Errorf("nil keep: %d violations in all, want 151", got)
	}
}
