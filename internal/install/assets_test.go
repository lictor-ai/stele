package install

import (
	"fmt"
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

// Agents write whole files through Bash, which skips stele (#50): every text
// stele hands the agent says to use Write and Edit.
func TestMessagesSayWriteAndEdit(t *testing.T) {
	const want = "Write files with Write and Edit only, not through Bash."
	for name, s := range map[string]string{
		"block":               BlockMessage("a.go", []BlockRule{{Text: "r"}}, ""),
		"violation":           ViolationMessage("a.go", []Violation{{Line: 1, Column: 1, RuleID: "r", Excerpt: "e", Message: "e"}}, 0, true),
		"violation, no block": ViolationMessage("a.go", []Violation{{Line: 1, Column: 1, RuleID: "r", Excerpt: "e", Message: "e"}}, 0, false),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("%s message lacks %q:\n%s", name, want, s)
		}
	}
}

func TestSessionContext(t *testing.T) {
	got := SessionContext()
	for _, want := range []string{"stele", "Write", "Edit", "Bash", "cat >", "sed -i"} {
		if !strings.Contains(got, want) {
			t.Errorf("session context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") || got != strings.TrimSpace(got) {
		t.Errorf("session context is not rendered clean:\n%q", got)
	}
}

// The templates are embedded as checked out; .gitattributes keeps them LF on
// every platform, so the agent never reads a stray \r.
func TestAssetsHaveLFLineEnds(t *testing.T) {
	for name, s := range map[string]string{"block-message": blockMessage, "violation-message": violationMessage, "stele-header.md": steleHeader, "ask-message": askMessage, "session-context": sessionContext} {
		if strings.Contains(s, "\r") {
			t.Errorf("%s.tmpl has CR line ends", name)
		}
	}
}

func TestAskMessage(t *testing.T) {
	got := AskMessage("sub/stele.json")
	for _, want := range []string{"sub/stele.json", "stele's own files", "asks every time", "only if you asked"} {
		if !strings.Contains(got, want) {
			t.Errorf("ask message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("ask message has template text left:\n%s", got)
	}
}

func TestViolationMessage(t *testing.T) {
	vs := []Violation{
		{Line: 3, Column: 7, RuleID: "r1", Excerpt: "Never put credentials in code.", Message: "Never put credentials in code."},
		{Line: 9, Column: 1, RuleID: "r2", Excerpt: "No TODO comments.", Message: "track it in an issue"},
	}
	got := ViolationMessage("internal/db.go", vs, 4, true)
	for _, want := range []string{
		"internal/db.go:3:7: rule r1 \"Never put credentials in code.\"",
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

	// A long rule message is cut, and the whole message stays under what
	// Claude Code passes on in full, cutting whole lines.
	long := strings.Repeat("m", 480)
	var many []Violation
	for i := range 30 {
		many = append(many, Violation{Line: i + 1, Column: 1, RuleID: "7f3a2b1c-0000-4000-8000-000000003b21", Excerpt: strings.Repeat("e", 60), Message: long})
	}
	big := ViolationMessage("internal/some/long/path/file.go", many, 2, true)
	if len(big) > MaxMessage {
		t.Errorf("message is %d bytes, want at most %d", len(big), MaxMessage)
	}
	if strings.Contains(big, strings.Repeat("m", 201)) || !strings.Contains(big, strings.Repeat("m", 200)+"…") {
		t.Error("a rule message is not cut at 200 characters")
	}
	shown := strings.Count(big, "\ninternal/some/long/path/file.go:")
	if shown == 30 || !strings.Contains(big, fmt.Sprintf("and %d more", 30-shown+2)) {
		t.Errorf("%d of 30 shown, want some cut and counted:\n%s", shown, big[len(big)-200:])
	}

	soft := ViolationMessage("a.go", vs[1:], 0, false)
	if !strings.Contains(soft, "non-blocking") || strings.Contains(soft, "more") {
		t.Errorf("non-blocking message:\n%s", soft)
	}
}
