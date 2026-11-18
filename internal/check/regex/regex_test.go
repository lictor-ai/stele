package regex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/index"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		content string
		want    []Location
	}{
		{"start of file", `foo`, "foo bar", []Location{{1, 1}}},
		{"mid line", `bar`, "foo bar", []Location{{1, 5}}},
		{"several on a line", `o`, "foo", []Location{{1, 2}, {1, 3}}},
		{"later lines", `x`, "a\nbx\n\nx", []Location{{2, 2}, {4, 1}}},
		{"CRLF", `x`, "a\r\nbx\r\n", []Location{{2, 2}}},
		{"multiline anchor", `(?m)^x`, "x\nax\nx", []Location{{1, 1}, {3, 1}}},
		{"no multiline: whole content", `^x`, "a\nx", nil},
		{"spans lines: where it starts", `a\nb`, "za\nb", []Location{{1, 2}}},
		{"no match", `q`, "abc", nil},

		// A column counts characters (code points), not bytes.
		{"column in characters", `x`, "éx", []Location{{1, 2}}},
		{"CJK and emoji are one each", `x`, "日本😀x", []Location{{1, 4}}},
		{"combining mark is its own character", `x`, "éx", []Location{{1, 3}}},
		{"tab is one", `x`, "\tx", []Location{{1, 2}}},
		{"multibyte before each of several matches", `x`, "éxéx", []Location{{1, 2}, {1, 4}}},
		{"multibyte on an earlier line does not count", `x`, "éé\nx", []Location{{2, 1}}},
		{"CRLF after multibyte", `x`, "é\r\nüx\r\n", []Location{{2, 2}}},
		{"CR is a character", `\r`, "é\r\n", []Location{{1, 2}}},

		// A leading byte order mark is not part of the text.
		{"BOM: column skips it", `x`, "\xEF\xBB\xBFx", []Location{{1, 1}}},
		{"BOM: multiline anchor on line 1", `(?m)^x`, "\uFEFFx\nx", []Location{{1, 1}, {2, 1}}},
		{"BOM: start of text", `\Ax`, "\uFEFFx", []Location{{1, 1}}},
		{"BOM: caret", `^x`, "\uFEFFx", []Location{{1, 1}}},
		{"BOM: later lines unaffected", `x`, "\uFEFFa\nbx", []Location{{2, 2}}},
		{"BOM: a pattern cannot match the leading one", `\x{FEFF}`, "\uFEFFa", nil},
		{"BOM: only one stripped", `x`, "\uFEFF\uFEFFx", []Location{{1, 2}}},
		{"U+FEFF mid-line counts", `x`, "a\uFEFFx", []Location{{1, 3}}},
		{"U+FEFF at the start of line 2 is not stripped", `(?m)^x`, "a\n\uFEFFx", nil},

		// Each byte that is not valid UTF-8 is one column, as the regexp steps.
		{"invalid UTF-8: one per byte", `x`, "\xffx", []Location{{1, 2}}},
		{"invalid UTF-8: truncated sequence", `x`, "\xe2\x82x", []Location{{1, 3}}},
		{"invalid then valid", `x`, "\xff\xfeéx", []Location{{1, 4}}},
		{"invalid byte before a newline", `x`, "\xc3\nx", []Location{{2, 1}}},
		{"match starting on an invalid byte", `.x`, "a\xffx", []Location{{1, 2}}},

		// A line ends at "\n" or "\r\n": with (?m), $ matches before either
		// (#147). A lone "\r" ends no line.
		{"(?m)$, LF", `(?m)bar$`, "foo bar\nbar\n", []Location{{1, 5}, {2, 1}}},
		{"(?m)$, CRLF", `(?m)bar$`, "foo bar\r\nbar\r\n", []Location{{1, 5}, {2, 1}}},
		{"(?m)$, mixed line ends", `(?m)bar$`, "bar\r\nbar\nbar\r\nbar", []Location{{1, 1}, {2, 1}, {3, 1}, {4, 1}}},
		{"(?m)$, lone CR ends no line", `(?m)bar$`, "bar\rbaz\nbar\rx", nil},
		{"(?m)$, CRLF after multibyte", `(?m)x$`, "é\r\néx\r\n", []Location{{2, 2}}},
		{"(?m)$, CRLF, case folded", `(?mi)BAR$`, "bar\r\n", []Location{{1, 1}}},
		{"(?m)$, CRLF, ungreedy", `(?mU)a+$`, "aa\r\n", []Location{{1, 1}}},
		{"(?m)$ inside a group, CRLF", `(?m)(?:x|y$)`, "ay\r\n", []Location{{1, 2}}},
		{"(?m).$, CRLF: the last character, not the CR", `(?m).$`, "ab\r\ncd\r\n", []Location{{1, 2}, {2, 2}}},
		{"(?m)$ then text, CRLF", `(?m)a$\s*^b`, "a\r\nb\r\n", []Location{{1, 1}}},
		{"(?m)^, CRLF", `(?m)^x`, "x\r\nax\r\nx", []Location{{1, 1}, {3, 1}}},
		{"no (?m): $ is the end of the file", `bar$`, "bar\r\nbar\r\n", nil},
		{"no (?m): end of file, CRLF before", `bar$`, "x\r\nbar", []Location{{2, 1}}},
		{"trailing blanks, CRLF", `(?m)[ \t]+$`, "a \r\nb\t\r\nc\r\n", []Location{{1, 2}, {2, 2}}},
		// A pattern that names the CR still sees it.
		{"names CR: CR at a line end", `(?m)\r$`, "a\r\nb\n", []Location{{1, 2}}},
		{"names CR: CRLF", `\r\n`, "a\nb\r\n", []Location{{2, 2}}},
		{"names CR: whitespace class takes it in", `(?m)\s+$`, "a\r\nb", []Location{{1, 2}}},
		{"names CR: negated class takes it in", `(?m)[^;]$`, "a;\r\nb;", []Location{{1, 3}}},
		{"escaped dollar is a dollar", `(?m)\$`, "a$\r\n", []Location{{1, 2}}},
		{"names CR: dot takes it in", `(?m)^.{3,}$`, "ab\r\nab\n", []Location{{1, 1}}},
		{"any character but a line end", `(?m)^[^\r\n]{3,}$`, "ab\r\nab\n", nil},
		{"(?m)$, lone CR at the end of the file ends the line", `(?m)bar$`, "bar\r", []Location{{1, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Match(mustCompile(t, tt.pattern), []byte(tt.content))
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Match (-want +got):\n%s", diff)
			}
		})
	}
}

func mustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

// A CRLF file is checked as its LF copy: same lines, same columns, for any
// pattern that cannot match the CR itself.
func TestMatchCRLFLikeLF(t *testing.T) {
	lf := "package a\n\nfunc f() { // TODO \n\treturn x;\n}\nlast line é;"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	for _, p := range []string{
		`(?m);$`, `(?m)[ \t]+$`, `(?m)^\t`, `(?m)^$`, `(?m)TODO.*$`, `(?m)TODO $`,
		`(?m)^}$`, `(?m)é;$`, `(?m)\w+$`, `(?m)(?:return|func).*$`,
		`(?m)^package \w+$`, `;\z`, `(?m)x;$|^func`, `(?ms)TODO.*^\}$`,
	} {
		re := mustCompile(t, p)
		want, got := Match(re, []byte(lf)), Match(re, []byte(crlf))
		if len(want) == 0 {
			t.Errorf("%s: no match in the LF file; the test checks nothing", p)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: CRLF (-LF +CRLF):\n%s", p, diff)
		}
	}
}

// Compile leaves a pattern with no (?m)$ as written.
func TestCompileLeavesOthers(t *testing.T) {
	for _, p := range []string{`foo`, `foo$`, `(?m)^foo`, `(?m)\$`, `(?m)[$]`, `a\z`} {
		if got := mustCompile(t, p).String(); got != p {
			t.Errorf("Compile(%q) = %q, want it unchanged", p, got)
		}
	}
	if _, err := Compile("("); err == nil {
		t.Error(`Compile("(") = nil error`)
	}

	// A pattern that compiles, but whose rewrite exceeds regexp's size limit,
	// is compiled as written.
	big := "(?m)(?:" + strings.Repeat("a$", 900) + "){1000}"
	if _, err := regexp.Compile(big); err != nil {
		t.Fatalf("the test needs a pattern that compiles: %v", err)
	}
	if got := mustCompile(t, big).String(); got != big {
		t.Errorf("Compile of a pattern too large to rewrite = %.40q…, want it as written", got)
	}
}

// Columns are counted once, going forward: recounting from the line start for
// each match would take about 10^10 steps here.
func TestMatchLongLine(t *testing.T) {
	const n = 100_000
	got := Match(regexp.MustCompile(`x`), []byte(strings.Repeat("éx", n)))
	if len(got) != n || got[n-1] != (Location{1, 2 * n}) {
		t.Fatalf("got %d locations, last %v; want %d, last {1 %d}", len(got), got[len(got)-1], n, 2*n)
	}
}

func rule(id string, meta map[string]string) check.Rule {
	return check.Rule{ID: id, Body: "Rule " + id + ".", Meta: meta}
}

func TestValidate(t *testing.T) {
	rules := []check.Rule{
		rule("missing", map[string]string{"check": "regex"}),
		rule("empty", map[string]string{"check": "regex", "pattern": ""}),
		rule("broken", map[string]string{"check": "regex", "pattern": "("}),
		rule("matches-nothing", map[string]string{"check": "regex", "pattern": "a*"}),
		rule("good", map[string]string{"check": "regex", "pattern": `password\s*=`}),
	}
	got, _ := New().Validate(context.Background(), rules)
	var ids []string
	for _, p := range got {
		ids = append(ids, p.RuleID)
		if p.Message == "" {
			t.Errorf("%s: empty message", p.RuleID)
		}
	}
	if diff := cmp.Diff([]string{"missing", "empty", "broken", "matches-nothing"}, ids); diff != "" {
		t.Errorf("problem rules (-want +got):\n%s", diff)
	}
	if !strings.Contains(got[2].Message, "missing closing )") {
		t.Errorf("broken pattern message = %q, want the regexp error", got[2].Message)
	}
}

func TestCheckContent(t *testing.T) {
	rules := []check.Rule{
		rule("plain", map[string]string{"pattern": "secret"}),
		rule("msg", map[string]string{"pattern": "TODO", "message": "file an issue"}),
		{ID: "md", Scope: []string{"*.md"}, Meta: map[string]string{"pattern": "secret"}},
	}
	got := New().CheckContent(rules, "a.go", []byte("x := secret\n// TODO\n"))
	want := []check.Violation{
		{RuleID: "plain", File: "a.go", Line: 1, Column: 6},
		{RuleID: "msg", File: "a.go", Line: 2, Column: 4, Message: "file an issue"},
	}
	if diff := cmp.Diff(check.Result{Violations: want}, got); diff != "" {
		t.Errorf("CheckContent (-want +got):\n%s", diff)
	}

	// A file starting with a byte order mark: line 1 is checked from its first
	// character, and columns count characters.
	bomRules := []check.Rule{
		rule("pkg", map[string]string{"pattern": `(?m)^package`}),
		rule("plain", map[string]string{"pattern": "secret"}),
	}
	got = New().CheckContent(bomRules, "a.go", []byte("\uFEFFpackage é // secret\n"))
	wantBOM := []check.Violation{
		{RuleID: "pkg", File: "a.go", Line: 1, Column: 1},
		{RuleID: "plain", File: "a.go", Line: 1, Column: 14},
	}
	if diff := cmp.Diff(check.Result{Violations: wantBOM}, got); diff != "" {
		t.Errorf("BOM file (-want +got):\n%s", diff)
	}

	// An excluded file gets nothing from the rule.
	noTests := []check.Rule{{ID: "go", Scope: []string{"**/*.go", "!**/*_test.go"}, Meta: map[string]string{"pattern": "secret"}}}
	if got := New().CheckContent(noTests, "a_test.go", []byte("secret\n")); len(got.Violations) != 0 {
		t.Errorf("excluded file: %+v, want no violations", got.Violations)
	}
	if got := New().CheckContent(noTests, "a.go", []byte("secret\n")); len(got.Violations) != 1 {
		t.Errorf("included file: %+v, want one violation", got.Violations)
	}

	// Binary: a NUL in the first 8 KB. Skipped, and said so.
	bin := append([]byte("secret\x00"), "secret"...)
	wantSkip := check.Result{Skipped: []check.Skip{{File: "a.bin", Reason: "binary"}}}
	if diff := cmp.Diff(wantSkip, New().CheckContent(rules, "a.bin", bin)); diff != "" {
		t.Errorf("binary file (-want +got):\n%s", diff)
	}
	// A NUL after the first 8 KB does not make it binary.
	late := append(bytes.Repeat([]byte("a"), 8<<10), "\x00secret"...)
	if got := New().CheckContent(rules[:1], "a.txt", late); len(got.Violations) != 1 || got.Skipped != nil {
		t.Errorf("late NUL: %+v, want one violation", got)
	}
}

func TestCheck(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "password = 1\n")
	writeFile(t, root, "sub/b.go", "ok\npassword = 2\n")
	writeFile(t, root, "big.go", "password = 3 "+strings.Repeat("x", 100))
	if err := os.Mkdir(filepath.Join(root, "dir.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkSkip := check.Skip{File: "link.go", Reason: "not a regular file"}
	if err := os.Symlink(filepath.Join(root, "a.go"), filepath.Join(root, "link.go")); err != nil {
		t.Logf("no symlinks here: %v", err)
		linkSkip.Reason = "no longer exists"
	}
	old := maxFile
	maxFile = 64
	t.Cleanup(func() { maxFile = old })

	rules := []check.Rule{rule("pw", map[string]string{"pattern": "password"})}
	files := []string{"a.go", "sub/b.go", "gone.go", "dir.go", "link.go", "big.go"}
	got, err := New().Check(context.Background(), root, rules, files)
	if err != nil {
		t.Fatal(err)
	}
	want := check.Result{
		Violations: []check.Violation{
			{RuleID: "pw", File: "a.go", Line: 1, Column: 1},
			{RuleID: "pw", File: "sub/b.go", Line: 2, Column: 1},
		},
		Skipped: []check.Skip{
			{File: "gone.go", Reason: "no longer exists"},
			{File: "dir.go", Reason: "not a regular file"},
			linkSkip,
			{File: "big.go", Reason: "larger than 64 bytes"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Check (-want +got):\n%s", diff)
	}
	if got := size(32 << 20); got != "32 MiB" {
		t.Errorf("size(32 MiB) = %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Check(ctx, root, rules, files); err == nil {
		t.Error("cancelled context: Check = nil, want an error")
	}
}

// stele check and the check after a write both treat "\r\n" as a line end
// (#147): the same rule flags a file whether its lines end in LF or CRLF.
func TestCheckCRLF(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "lf.txt", "foo\nfoo bar\nbaz\n")
	writeFile(t, root, "crlf.txt", "foo\r\nfoo bar\r\nbaz\r\n")
	rules := []check.Rule{rule("bar", map[string]string{"pattern": `(?m)bar$`})}
	got, err := New().Check(context.Background(), root, rules, []string{"crlf.txt", "lf.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := check.Result{Violations: []check.Violation{
		{RuleID: "bar", File: "crlf.txt", Line: 2, Column: 5},
		{RuleID: "bar", File: "lf.txt", Line: 2, Column: 5},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Check (-want +got):\n%s", diff)
	}
}

// After an Edit, only the lines it wrote are checked; in a CRLF file, an
// edit's line found from its LF text, and a violation on it found by (?m)$.
func TestRunFileLinesCRLF(t *testing.T) {
	idx := index.Index{Rules: []index.Entry{{
		ID: "semi", Body: "No semicolons.", Meta: map[string]string{"check": Name, "pattern": `(?m);$`},
	}}}
	cs := map[string]check.Checker{Name: New()}
	for _, nl := range []string{"\n", "\r\n"} {
		content := []byte(strings.Join([]string{"a;", "b", "c := 1;", "d;", ""}, nl))
		spans := check.Spans(content, []check.Fragment{{Text: "b\nc := 1;\n"}})
		keep := func(line int) bool { return check.Contain(spans, line) }
		r := check.RunFileLines(idx, "a.go", content, cs, keep)
		var got []Location
		for _, v := range r.Violations {
			got = append(got, Location{Line: v.Line, Column: v.Column})
		}
		if diff := cmp.Diff([]Location{{Line: 3, Column: 7}}, got); diff != "" {
			t.Errorf("line ends %q: violations (-want +got):\n%s", nl, diff)
		}
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
