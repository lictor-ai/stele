package install

import (
	"strings"
	"testing"
)

func TestBlockMessage(t *testing.T) {
	got := BlockMessage("internal/db/query.go", []BlockRule{
		{ID: "a", Text: "Never build SQL by string concatenation."},
		{ID: "b", Text: "Log through the shared logger."},
	}, "")
	for _, want := range []string{
		"internal/db/query.go",
		"retry the same write",
		"Never build SQL by string concatenation.",
		"Log through the shared logger.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Note:") {
		t.Errorf("message has a note when none was given:\n%s", got)
	}
	if note := BlockMessage("x.go", []BlockRule{{Text: "r"}}, "stele.md:3: rule has no body"); !strings.Contains(note, "Note: stele.md:3: rule has no body") {
		t.Errorf("note missing:\n%s", note)
	}
}
