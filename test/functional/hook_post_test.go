package functional

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const checkRules = `scope: ["**/*.go"]
check: regex
pattern: fmt\.Print(f|ln)?\(
message: log through internal/log

Never print with fmt.Print.

---

check: regex
pattern: AKIA[0-9A-Z]{16}
message: no credentials

Never put credentials in code.

---

scope: ["**/*.go"]
check: regex
pattern: \bpanic\(
blocking: false
message: prefer returning an error

Prefer returning errors to panic.
`

// locations pulls file:line:col out of a violation message.
var locations = regexp.MustCompile(`(?m)^(\S+?):(\d+):(\d+): rule `)

func lines(msg string) []string {
	var out []string
	for _, m := range locations.FindAllStringSubmatch(msg, -1) {
		out = append(out, m[2]+":"+m[3])
	}
	return out
}

func TestPostWriteReportsExactLines(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	src := "package a\n\nimport \"fmt\"\n\nfunc f() {\n\tfmt.Println(\"hi\")\n\tkey := \"AKIAIOSFODNN7EXAMPLE\"\n\t_ = key\n\tpanic(1)\n}\n"
	p.write("a.go", src)
	a := p.hook(post("a.go", src))
	if !a.Block {
		t.Fatalf("blocking violations were not sent back as a block:\n%s", a.Raw)
	}
	if got := lines(a.Reason); strings.Join(got, ",") != "6:2,7:10" {
		t.Errorf("violation positions = %v, want [6:2 7:10]\n%s", got, a.Reason)
	}
	if !strings.Contains(a.Context, "prefer returning an error") || strings.Contains(a.Reason, "prefer returning") {
		t.Errorf("the non-blocking violation belongs in context, not in the block:\nreason: %s\ncontext: %s", a.Reason, a.Context)
	}
	measure("post_block_message_bytes", len(a.Reason)+len(a.Context))
}

// A clean write says nothing at all: no tokens spent.
func TestPostCleanWriteIsSilent(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	src := "package a\n\nfunc f() error { return nil }\n"
	p.write("a.go", src)
	if a := p.hook(post("a.go", src)); !a.silent() {
		t.Errorf("clean write produced output (costs tokens):\n%s", a.Raw)
	}
}

// CRLF files (Windows checkouts) must report the same line numbers.
func TestPostCRLF(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	src := "package a\r\n\r\nfunc f() {\r\n\tfmt.Println(1)\r\n}\r\n"
	p.write("a.go", src)
	a := p.hook(post("a.go", src))
	if got := lines(a.Reason); len(got) != 1 || got[0] != "4:2" {
		t.Errorf("CRLF file: positions %v, want [4:2]\n%s", got, a.Raw)
	}
}

// A byte order mark must not shift columns on line 1, or hide a violation.
func TestPostBOM(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	src := "\xef\xbb\xbfAKIAIOSFODNN7EXAMPLE\n"
	p.write("keys.txt", src)
	a := p.hook(post("keys.txt", src))
	if got := lines(a.Reason); len(got) != 1 || got[0] != "1:1" {
		t.Errorf("BOM file: positions %v, want [1:1]\n%s", got, a.Raw)
	}
}

// Columns after non-ASCII text: editors and CI annotations count characters
// (or UTF-16 units), not bytes. Recorded, not enforced: the spec does not say.
func TestPostColumnAfterUnicode(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	src := "// héllo wörld 🎉 AKIAIOSFODNN7EXAMPLE\n"
	p.write("notes.txt", src)
	got := lines(p.hook(post("notes.txt", src)).Reason)
	measure("column_after_unicode", got)
	if len(got) == 1 && got[0] != "1:18" {
		t.Logf("FINDING: column is %s; counting characters it would be 1:18 (bytes give 1:23)", got[0])
	}
}

// After an Edit, only the lines it wrote count. The file's older violations
// are left alone, so the agent is not pushed to rewrite code outside its task.
func TestPostEditOnlyNewLines(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	old := "package a\n\nfunc legacy() {\n\tfmt.Println(\"old\")\n}\n\nfunc f() {\n\t_ = 0\n}\n"
	p.write("a.go", old)
	p.commit()

	t.Run("clean edit beside an old violation is silent", func(t *testing.T) {
		p := p.on(t)
		now := strings.Replace(old, "_ = 0", "_ = 1", 1)
		p.write("a.go", now)
		e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "_ = 0", New: "_ = 1",
			Patch: []hunk{{OldStart: 7, OldLines: 3, NewStart: 7, NewLines: 3, Lines: []string{" func f() {", "-\t_ = 0", "+\t_ = 1", " }"}}}}
		if a := p.hook(e); a.Block {
			t.Errorf("an old violation on line 4 was blamed on an edit of line 8:\n%s", a.Reason)
		}
	})
	t.Run("violating edit is reported at its line", func(t *testing.T) {
		p := p.on(t)
		now := strings.Replace(old, "_ = 0", "fmt.Printf(\"x\")", 1)
		p.write("a.go", now)
		e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "_ = 0", New: "fmt.Printf(\"x\")",
			Patch: []hunk{{OldStart: 7, OldLines: 3, NewStart: 7, NewLines: 3, Lines: []string{" func f() {", "-\t_ = 0", "+\tfmt.Printf(\"x\")", " }"}}}}
		a := p.hook(e)
		if got := lines(a.Reason); !a.Block || len(got) != 1 || got[0] != "8:2" {
			t.Errorf("want one block at 8:2, got %v\n%s", got, a.Raw)
		}
	})
	t.Run("no patch: new text located in the file", func(t *testing.T) {
		p := p.on(t)
		now := strings.Replace(old, "_ = 0", "fmt.Print(2)", 1)
		p.write("a.go", now)
		e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "_ = 0", New: "fmt.Print(2)"}
		a := p.hook(e)
		if got := lines(a.Reason); !a.Block || len(got) != 1 || got[0] != "8:2" {
			t.Errorf("without a patch, want one block at 8:2, got %v\n%s", got, a.Raw)
		}
	})
	t.Run("a patch that does not add up falls back on new_string", func(t *testing.T) {
		p := p.on(t)
		now := strings.Replace(old, "_ = 0", "fmt.Print(3)", 1)
		p.write("a.go", now)
		e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "_ = 0", New: "fmt.Print(3)",
			Patch: []hunk{{OldStart: 1, OldLines: 99, NewStart: 1, NewLines: 99, Lines: []string{"+bogus"}}}}
		a := p.hook(e)
		if got := lines(a.Reason); !a.Block || len(got) != 1 || got[0] != "8:2" {
			t.Errorf("bogus patch: want one block at 8:2, got %v\n%s", got, a.Raw)
		}
	})
	t.Run("an edit that adds a second copy of an old violation", func(t *testing.T) {
		p := p.on(t)
		// The new text equals text already in the file. Without a patch,
		// stele cannot tell which copy is new; README limit 6 says it
		// checks nothing. With a patch it must catch it.
		now := strings.Replace(old, "_ = 0", "fmt.Println(\"old\")", 1)
		p.write("a.go", now)
		e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "_ = 0", New: "fmt.Println(\"old\")",
			Patch: []hunk{{OldStart: 7, OldLines: 3, NewStart: 7, NewLines: 3, Lines: []string{" func f() {", "-\t_ = 0", "+\tfmt.Println(\"old\")", " }"}}}}
		a := p.hook(e)
		if got := lines(a.Reason); !a.Block || len(got) != 1 || got[0] != "8:2" {
			t.Errorf("with a patch, the copy on line 8 must be caught, and only it: %v\n%s", got, a.Raw)
		}
		e.Patch = nil
		if a := p.hook(e); a.Block {
			t.Logf("without a patch, an ambiguous copy was reported: %s", a.Reason)
		} else {
			t.Logf("FINDING (documented, limit 6): without a patch, a duplicated violation is not checked")
		}
	})
}

// replace_all writes the same text in many places: each one is new.
func TestPostEditReplaceAll(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	now := "package a\n\nfunc a() { fmt.Println(1) }\nfunc b() { fmt.Println(1) }\nfunc c() { fmt.Println(1) }\n"
	p.write("a.go", now)
	e := event{Name: "PostToolUse", Prompt: "t", Tool: "Edit", File: "a.go", Old: "log(1)", New: "fmt.Println(1)", ReplaceAll: true}
	if got := lines(p.hook(e).Reason); len(got) != 3 {
		t.Errorf("replace_all wrote 3 violations, %d reported: %v", len(got), got)
	}
}

func TestPostMultiEdit(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	now := "package a\n\nfunc a() { fmt.Println(\"one\") }\n\nfunc b() { x := 1; _ = x }\n\nfunc c() { fmt.Printf(\"two\") }\n"
	p.write("a.go", now)
	e := event{Name: "PostToolUse", Prompt: "t", Tool: "MultiEdit", File: "a.go",
		Edits: []string{"func a() { fmt.Println(\"one\") }", "func b() { x := 1; _ = x }", "func c() { fmt.Printf(\"two\") }"}}
	if got := lines(p.hook(e).Reason); strings.Join(got, ",") != "3:12,7:12" {
		t.Errorf("MultiEdit positions %v, want [3:12 7:12]", got)
	}
}

// Many violations: the agent gets at most 20, the rest counted, and the
// message stays under what Claude Code passes on in full (10,000 chars).
func TestPostCapsTheMessage(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()
	var b strings.Builder
	b.WriteString("package a\n\nfunc f() {\n")
	for i := range 500 {
		fmt.Fprintf(&b, "\tfmt.Println(%d)\n", i)
	}
	b.WriteString("}\n")
	p.write("a.go", b.String())
	a := p.hook(post("a.go", b.String()))
	n := len(lines(a.Reason))
	if !a.Block || n == 0 || n > 20 {
		t.Errorf("500 violations: %d shown, want 1..20", n)
	}
	if !strings.Contains(a.Reason, "more") {
		t.Errorf("the hidden violations were not counted:\n%s", a.Reason)
	}
	if len([]rune(a.Reason)) > 10000 {
		t.Errorf("block reason is %d chars; Claude Code truncates past 10,000", len([]rune(a.Reason)))
	}
	measure("post_500_violations", map[string]int{"shown": n, "bytes": len(a.Reason), "est_tokens": tokens(a.Reason)})
}

// Long rule text in excerpts and messages must not blow the budget either.
func TestPostHugeMessagesStayBounded(t *testing.T) {
	long := strings.Repeat("very long message ", 2000)
	md := "check: regex\npattern: BAD\nmessage: " + long + "\n\n" + strings.Repeat("Long rule body. ", 2000) + "\n"
	p := newProject(t, md)
	p.compile()
	src := strings.Repeat("BAD\n", 50)
	p.write("a.txt", src)
	a := p.hook(post("a.txt", src))
	if !a.Block {
		t.Fatalf("not blocked:\n%.500s", a.Raw)
	}
	if len([]rune(a.Reason)) > 10000 {
		t.Errorf("block reason is %d chars with a long message; Claude Code truncates past 10,000", len([]rune(a.Reason)))
	}
}

// Files stele cannot check say so instead of passing silently.
func TestPostUncheckableFiles(t *testing.T) {
	p := newProject(t, checkRules)
	p.compile()

	t.Run("binary", func(t *testing.T) {
		p := p.on(t)
		data := "AKIAIOSFODNN7EXAMPLE\x00\x01\x02binary"
		p.write("blob.bin", data)
		a := p.hook(post("blob.bin", data))
		if a.Block {
			t.Logf("binary file was checked and blocked: %s", a.Reason)
		} else if !strings.Contains(a.Context+a.Warning, "blob.bin") {
			t.Errorf("binary file skipped silently; README limit 7 says the skip is reported:\n%s", a.Raw)
		}
	})
	t.Run("over 32 MiB", func(t *testing.T) {
		p := p.on(t)
		f := p.path("big.txt")
		fh, err := os.Create(f)
		if err != nil {
			t.Fatal(err)
		}
		fh.WriteString("AKIAIOSFODNN7EXAMPLE\n")
		fh.Truncate(33 << 20)
		fh.Close()
		a := p.hook(post("big.txt", "AKIAIOSFODNN7EXAMPLE"))
		if a.Block || !strings.Contains(a.Context+a.Warning, "big.txt") {
			t.Errorf("a file over 32 MiB must be skipped and the skip reported:\n%.500s", a.Raw)
		}
	})
	t.Run("deleted before the hook ran", func(t *testing.T) {
		p := p.on(t)
		if a := p.hook(post("gone.go", "fmt.Println()")); a.Block {
			t.Errorf("a file that no longer exists was blocked:\n%s", a.Raw)
		}
	})
	t.Run("symlink out of the project", func(t *testing.T) {
		p := p.on(t)
		outside := filepath.Join(t.TempDir(), "secret.txt")
		os.WriteFile(outside, []byte("AKIAIOSFODNN7EXAMPLE\n"), 0o644)
		if err := os.Symlink(outside, p.path("link.txt")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("symlinks need developer mode on Windows")
			}
			t.Fatal(err)
		}
		a := p.hook(post("link.txt", "x"))
		if a.Block {
			t.Logf("symlink target was read and checked")
		} else if !strings.Contains(a.Context+a.Warning, "link.txt") {
			t.Errorf("symlink skipped silently:\n%s", a.Raw)
		}
	})
}

// Regex limits from the README: the pattern runs over the whole file, so ^
// and $ need (?m); and a check fires inside comments and strings too.
func TestPostRegexSemantics(t *testing.T) {
	md := "check: regex\npattern: (?m)^TODO\n\nNo TODO at line start.\n"
	p := newProject(t, md)
	p.compile()
	src := "a\nTODO fix\n  // TODO indented\n"
	p.write("a.txt", src)
	if got := lines(p.hook(post("a.txt", src)).Reason); strings.Join(got, ",") != "2:1" {
		t.Errorf("(?m)^TODO positions %v, want [2:1]", got)
	}
}

// A pattern that matches the empty string is an invalid rule: stele check
// fails on it. The developer should hear about it before CI, and the agent
// must not be left unchecked without anyone being told.
func TestInvalidRuleIsNotSilent(t *testing.T) {
	p := newProject(t, "check: regex\npattern: x*\n\nAnything.\n")
	code, out, errOut := p.stele("compile")
	compileSaid := code != 0 || strings.Contains(out+errOut, "invalid")
	src := "an x here\n"
	p.write("a.txt", src)
	a := p.hook(post("a.txt", src))
	hookSaid := !a.silent()
	p.commit()
	if code, _, _ := p.stele("check", "--all"); code != 1 {
		t.Errorf("stele check: exit %d on an invalid rule, want 1", code)
	}
	if !compileSaid && !hookSaid {
		t.Logf("FINDING: stele compile accepts an invalid rule (pattern x*) with exit 0 and no warning, and the hook skips it silently; only stele check reports it")
	}
}

// A backstop-off project gets violations as context, never as a block.
func TestPostBackstopOffNeverBlocks(t *testing.T) {
	p := newProject(t, checkRules)
	p.write("stele.json", `{"backstop": false}`)
	p.compile()
	src := "package a\nfunc f() { fmt.Println(1) }\n"
	p.write("a.go", src)
	a := p.hook(post("a.go", src))
	if a.Block || !strings.Contains(a.Context, "a.go:2") {
		t.Errorf("backstop off: want context with the violation and no block:\n%s", a.Raw)
	}
}
