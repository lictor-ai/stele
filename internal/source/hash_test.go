package source

import "testing"

func TestHash(t *testing.T) {
	same := []struct{ name, a, b string }{
		{"case", "Use parameterised queries.", "use PARAMETERISED queries."},
		{"whitespace runs", "Use  parameterised\tqueries.", "Use parameterised queries."},
		{"rewrapped", "Use parameterised\nqueries.", "Use parameterised queries."},
		{"blank lines", "Use parameterised\n\n\nqueries.", "Use parameterised queries."},
		{"CRLF", "Use parameterised\r\nqueries.\r\n", "Use parameterised\nqueries.\n"},
		{"edge punctuation", "- Use parameterised queries!", "Use parameterised queries"},
	}
	for _, tt := range same {
		t.Run(tt.name, func(t *testing.T) {
			if Hash(tt.a) != Hash(tt.b) {
				t.Errorf("Hash(%q) != Hash(%q)", tt.a, tt.b)
			}
		})
	}

	different := []struct{ name, a, b string }{
		{"words", "Use parameterised queries.", "Never use parameterised queries."},
		{"inner punctuation", "Use a.b", "Use ab"},
	}
	for _, tt := range different {
		t.Run(tt.name, func(t *testing.T) {
			if Hash(tt.a) == Hash(tt.b) {
				t.Errorf("Hash(%q) == Hash(%q)", tt.a, tt.b)
			}
		})
	}
}
