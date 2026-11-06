package printable

import "testing"

func TestClean(t *testing.T) {
	cases := map[string]string{
		"plain":               "plain",
		"two\nlines\tand tab": "two lines and tab",
		"\x1b[31mred\x1b[0m":  "red",
		"bell\a and \x00nul":  "bell and nul",
		// U+202E (right-to-left override), U+2066 (left-to-right isolate)
		"left\xe2\x80\xaeright\xe2\x81\xa6x": "leftrightx",
		// U+200E, U+200F (direction marks)
		"mark\xe2\x80\x8e\xe2\x80\x8fs": "marks",
		"unicode \xc3\xa9 stays":        "unicode \xc3\xa9 stays",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinesKeepsBreaks(t *testing.T) {
	if got := Lines("a\x1b[1m\nb\tc\n"); got != "a\nb c\n" {
		t.Errorf("Lines = %q", got)
	}
}
