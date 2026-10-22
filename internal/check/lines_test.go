package check

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/index"
)

func TestSpans(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		fragments []string
		want      []Span
	}{
		{name: "one line", content: "a\nb\nc\n", fragments: []string{"b"}, want: []Span{{2, 2}}},
		{name: "several lines", content: "a\nb\nc\nd\n", fragments: []string{"b\nc"}, want: []Span{{2, 3}}},
		{name: "a fragment ending in a newline stops at its line", content: "a\nb\nc\n", fragments: []string{"b\n"}, want: []Span{{2, 2}}},
		{name: "every occurrence (replace_all)", content: "x\ny\nx\n", fragments: []string{"x"}, want: []Span{{1, 1}, {3, 3}}},
		{name: "not found", content: "a\nb\n", fragments: []string{"z"}, want: nil},
		{name: "empty fragment", content: "a\nb\n", fragments: []string{""}, want: nil},
		{name: "at the start and the end, no final newline", content: "start\nmid\nend", fragments: []string{"start", "end"}, want: []Span{{1, 1}, {3, 3}}},
		{name: "a CRLF file and an LF fragment", content: "a\r\nb\r\nc\r\n", fragments: []string{"b\nc"}, want: []Span{{2, 3}}},
		{name: "several fragments", content: "a\nb\nc\nd\n", fragments: []string{"d", "b"}, want: []Span{{2, 2}, {4, 4}}},
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

func TestSpansContain(t *testing.T) {
	s := Spans([]byte("a\nb\nc\nd\n"), []string{"b\nc"})
	for line, want := range map[int]bool{1: false, 2: true, 3: true, 4: false} {
		if got := Contain(s, line); got != want {
			t.Errorf("Contain(%d) = %v, want %v", line, got, want)
		}
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
