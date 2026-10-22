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

func TestViolationMessage(t *testing.T) {
	vs := []Violation{
		{Line: 3, Column: 7, RuleID: "r1", Excerpt: "Never put credentials in code.", Message: "Never put credentials in code."},
		{Line: 9, Column: 1, RuleID: "r2", Excerpt: "No TODO comments.", Message: "track it in an issue"},
	}
	got := ViolationMessage("internal/db.go", vs, 4, true)
	for _, want := range []string{
		"internal/db.go:3:7: rule r1 \"Never put credentials in code.\"\n",
		"internal/db.go:9:1: rule r2 \"No TODO comments.\": track it in an issue",
		"and 4 more",
		"stele check internal/db.go",
		"fix",
		"was saved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("blocking message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "credentials in code.\": Never") {
		t.Errorf("a message equal to the excerpt is printed twice:\n%s", got)
	}

	soft := ViolationMessage("a.go", vs[1:], 0, false)
	if !strings.Contains(soft, "non-blocking") || strings.Contains(soft, "more") {
		t.Errorf("non-blocking message:\n%s", soft)
	}
}
