package claudecode

import (
	"strings"
	"testing"
)

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
	for name, s := range map[string]string{"session-context": sessionContext, "rules-file": rulesFile} {
		if strings.Contains(s, "\r") {
			t.Errorf("%s.tmpl has CR line ends", name)
		}
	}
}
