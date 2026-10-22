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
		{"column in bytes", `x`, "éx", []Location{{1, 3}}},
		{"multiline anchor", `(?m)^x`, "x\nax\nx", []Location{{1, 1}, {3, 1}}},
		{"no multiline: whole content", `^x`, "a\nx", nil},
		{"spans lines: where it starts", `a\nb`, "za\nb", []Location{{1, 2}}},
		{"no match", `q`, "abc", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Match(regexp.MustCompile(tt.pattern), []byte(tt.content))
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Match (-want +got):\n%s", diff)
			}
		})
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
	got := New().Validate(rules)
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
