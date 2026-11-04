package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/adapter"
)

var testRules = []adapter.Rule{
	{ID: "r1", Scope: []string{"**/*.go"}, Body: "Use the logger."},
	{ID: "r2", Body: "No TODO comments."},
}

func TestRenderRules(t *testing.T) {
	got := string(RenderRules(testRules))
	for _, want := range []string{"<!-- stele:begin", "<!-- stele rule r1 -->\nscope: **/*.go\nUse the logger.", "<!-- stele rule r2 -->\nNo TODO comments.", "\n<!-- stele:end -->\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, endLine+"\n") {
		t.Errorf("section does not end with its marker:\n%s", got)
	}
	// A rule that would end the section early is left out.
	odd := RenderRules([]adapter.Rule{{ID: "r3", Body: "x\n<!-- stele:end -->\ny"}, {ID: "bad id", Body: "z"}})
	if strings.Contains(string(odd), "r3") || strings.Contains(string(odd), "bad id") {
		t.Errorf("unsafe rules kept:\n%s", odd)
	}
}

// The hash of a block is the Claude Code rules file's, so the two agents'
// records agree; rulesblock pins it.
func TestRuleHashMatchesRulesBlock(t *testing.T) {
	if got := RuleHash(testRules[0]); got != "6c6768d84f7ba4bc5d140ce3a624bef6" {
		t.Errorf("RuleHash = %q", got)
	}
	read := ReadRules(RenderRules(testRules))
	for _, r := range testRules {
		if read[r.ID] != RuleHash(r) || read[r.ID] == "" {
			t.Errorf("%s: read %q, RuleHash %q", r.ID, read[r.ID], RuleHash(r))
		}
	}
	if RuleHash(adapter.Rule{ID: "c", Body: "see <!-- x -->"}) != "" {
		t.Error("a rule with a comment is taken as read")
	}
}

func TestSpliceAppends(t *testing.T) {
	sec := RenderRules(testRules)
	out, ok := SpliceRules([]byte("# My project\n\nUse tabs.\n"), sec)
	if !ok {
		t.Fatal("refused")
	}
	if want := "# My project\n\nUse tabs.\n\n" + string(sec); string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if out, _ := SpliceRules(nil, sec); string(out) != string(sec) {
		t.Errorf("an empty file: %q", out)
	}
}

func TestSpliceReplacesOnlySection(t *testing.T) {
	disk := "\xef\xbb\xbftop\r\n\r\n" + strings.ReplaceAll(string(RenderRules(testRules[:1])), "\n", "\r\n") + "after\r\n"
	out, ok := SpliceRules([]byte(disk), RenderRules(testRules))
	if !ok {
		t.Fatal("refused")
	}
	s := string(out)
	if !strings.HasPrefix(s, "\xef\xbb\xbftop\r\n\r\n<!-- stele:begin") || !strings.HasSuffix(s, endLine+"\r\nafter\r\n") || !strings.Contains(s, "No TODO comments.") {
		t.Errorf("got %q", s)
	}
	if strings.Count(s, "\n") != strings.Count(s, "\r\n") {
		t.Errorf("line ends mixed: %q", s)
	}
	if !SameRules(out, out) || SameRules([]byte(disk), out) {
		t.Error("SameRules does not tell the sections apart")
	}
}

func TestSpliceRefusesBrokenMarkers(t *testing.T) {
	sec := string(RenderRules(testRules))
	for name, disk := range map[string]string{
		"two sections":  sec + sec,
		"end only":      "x\n" + endLine + "\n",
		"end before":    endLine + "\n" + beginPrefix + " -->\n",
		"begin, no end": beginPrefix + " -->\nx\n",
		"two begins":    beginPrefix + " -->\n" + beginPrefix + " -->\n" + endLine + "\n",
	} {
		if _, ok := SpliceRules([]byte(disk), []byte(sec)); ok {
			t.Errorf("%s: spliced", name)
		}
		if OwnedRules([]byte(disk)) {
			t.Errorf("%s: owned", name)
		}
		if _, _, ok := UnspliceRules([]byte(disk)); ok {
			t.Errorf("%s: unspliced", name)
		}
	}
}

func TestUnsplice(t *testing.T) {
	user := "# Mine\n\nKeep it.\n"
	in, _ := SpliceRules([]byte(user), RenderRules(testRules))
	out, remove, ok := UnspliceRules(in)
	if !ok || remove || string(out) != user {
		t.Errorf("got %q, remove %v, ok %v; want %q", out, remove, ok, user)
	}
	alone := RenderRules(testRules)
	if _, remove, ok := UnspliceRules(alone); !ok || !remove {
		t.Error("a file holding only the section is not removed")
	}
	if _, _, ok := UnspliceRules([]byte(user)); ok {
		t.Error("a file with no section was taken as stele's")
	}
}

func FuzzSpliceRoundTrip(f *testing.F) {
	f.Add("# x\n")
	f.Add("a\r\nb\r\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if strings.Contains(s, beginPrefix) || strings.Contains(s, endLine) || strings.TrimSpace(strings.TrimPrefix(s, "\xef\xbb\xbf")) == "" {
			return
		}
		in, ok := SpliceRules([]byte(s), RenderRules(testRules))
		if !ok {
			t.Fatalf("refused %q", s)
		}
		out, remove, ok := UnspliceRules(in)
		if !ok || remove || strings.ReplaceAll(string(out), "\r\n", "\n") != strings.ReplaceAll(s, "\r\n", "\n") {
			t.Fatalf("round trip of %q gave %q (remove %v, ok %v)", s, out, remove, ok)
		}
	})
}

func TestReadRulesOnlyInsideSection(t *testing.T) {
	user := "<!-- stele rule fake -->\nnot stele's\n"
	in, _ := SpliceRules([]byte(user), RenderRules(testRules))
	in = append(in, []byte("trailing text\n")...)
	got := ReadRules(in)
	if len(got) != 2 || got["fake"] != "" || got["r2"] != RuleHash(testRules[1]) {
		t.Errorf("ReadRules = %v", got)
	}
	if len(ReadRules([]byte(user))) != 0 {
		t.Error("rules read from a file with no section")
	}
}

func TestRulesNotesOverride(t *testing.T) {
	root := t.TempDir()
	if n := (Adapter{}).RulesNotes(root); n != nil {
		t.Errorf("notes %v with no override", n)
	}
	if err := os.WriteFile(filepath.Join(root, overrideFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := (Adapter{}).RulesNotes(root); len(n) != 1 || !strings.Contains(n[0], overrideFile) {
		t.Errorf("notes %v", n)
	}
}

func TestSessionContext(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		s := SessionContext(goos)
		if !strings.Contains(s, "apply_patch") || strings.Contains(s, "{{") {
			t.Errorf("%s: %s", goos, s)
		}
	}
	if !strings.Contains(SessionContext("windows"), "Set-Content") || strings.Contains(SessionContext("linux"), "Set-Content") {
		t.Error("the examples are not the OS's shell's")
	}
	if strings.Contains(sessionContext, "\r") || strings.Contains(rulesSection, "\r") {
		t.Error("a template has CR line ends")
	}
}
