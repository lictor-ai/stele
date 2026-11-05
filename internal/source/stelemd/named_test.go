package stelemd

import (
	"errors"
	"testing"

	"github.com/lictor-ai/stele/internal/source"
)

func TestParseNamed(t *testing.T) {
	rules, err := ParseNamed([]byte("One.\n\n---\n\nTwo.\n"), "host/g/stele.md@abc1234")
	if err != nil || len(rules) != 2 {
		t.Fatalf("ParseNamed = %+v, %v", rules, err)
	}
	for _, r := range rules {
		if r.File != "host/g/stele.md@abc1234" {
			t.Errorf("rule %d File = %q", r.Position, r.File)
		}
	}
	_, err = ParseNamed([]byte("```\nopen\n"), "host/g/stele.md@abc1234")
	var pe *source.ParseError
	if !errors.As(err, &pe) || pe.File != "host/g/stele.md@abc1234" || pe.Line != 1 {
		t.Errorf("ParseNamed error = %v, want one naming the file at line 1", err)
	}
}
