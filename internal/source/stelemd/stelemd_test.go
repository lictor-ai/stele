package stelemd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/source"
)

// rule fills in the fields every stele.md rule shares.
func rule(position, line int, r source.Rule) source.Rule {
	r.Source = "stelemd"
	r.File = "stele.md"
	r.Position = position
	r.Line = line
	return r
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []source.Rule
	}{
		{name: "empty file", src: "", want: nil},
		{name: "only separators and blank lines", src: "\n---\n\n---\n", want: nil},
		{
			name: "no metadata",
			src:  "Rules with no metadata are just prose.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "Rules with no metadata are just prose."})},
		},
		{
			name: "spec example",
			src: `tags: [db, security]
scope: ["**/*.go"]

Never build SQL by string concatenation. Use parameterised queries.

---

tags: [http]

Every HTTP handler must go through the standard middleware chain.

---

Rules with no metadata are just prose. This is a valid rule.
`,
			want: []source.Rule{
				rule(0, 1, source.Rule{
					Tags:  []string{"db", "security"},
					Scope: []string{"**/*.go"},
					Body:  "Never build SQL by string concatenation. Use parameterised queries.",
				}),
				rule(1, 8, source.Rule{
					Tags: []string{"http"},
					Body: "Every HTTP handler must go through the standard middleware chain.",
				}),
				rule(2, 14, source.Rule{Body: "Rules with no metadata are just prose. This is a valid rule."}),
			},
		},
		{
			name: "unknown keys kept in meta",
			src: `tags: [security]
check: regex
pattern: (?i)(password|api_key|secret)\s*[:=]\s*"[^"]{8,}"
owner: "platform-team"

Never put credentials in source code.
`,
			want: []source.Rule{rule(0, 1, source.Rule{
				Tags: []string{"security"},
				Body: "Never put credentials in source code.",
				Meta: map[string]string{
					"check":   "regex",
					"pattern": `(?i)(password|api_key|secret)\s*[:=]\s*"[^"]{8,}"`,
					"owner":   `"platform-team"`,
				},
			})},
		},
		{
			name: "stele's keys are unquoted, checker keys kept as written",
			src:  "check: \"regex\"\nblocking: 'false'\npattern: \"net/http\"\nmessage: \"net\" is banned, use \"x\"\nowner: \"platform-team\"\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{
				Body: "Body.",
				Meta: map[string]string{
					"check":    "regex",
					"blocking": "false",
					"pattern":  `"net/http"`,
					"message":  `"net" is banned, use "x"`,
					"owner":    `"platform-team"`,
				},
			})},
		},
		{
			name: "description",
			src:  "description: 'Use parameterised queries'\n\nA long rule.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Description: "Use parameterised queries", Body: "A long rule."})},
		},
		{
			name: "body opening with a capitalised key is body",
			src:  "Note: this is prose.\nSo is this.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "Note: this is prose.\nSo is this."})},
		},
		{
			name: "body opening with a url is body",
			src:  "https://example.com explains why.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "https://example.com explains why."})},
		},
		{
			name: "body keeps inner blank lines and indentation",
			src:  "tags: [go]\n\n\nFirst paragraph.\n\n    indented code\n\nLast.   \n\n",
			want: []source.Rule{rule(0, 1, source.Rule{
				Tags: []string{"go"},
				Body: "First paragraph.\n\n    indented code\n\nLast.",
			})},
		},
		{
			name: "crlf",
			src:  "tags: [a]\r\n\r\nLine one.\r\nLine two.\r\n---\r\nSecond.\r\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Tags: []string{"a"}, Body: "Line one.\nLine two."}),
				rule(1, 6, source.Rule{Body: "Second."}),
			},
		},
		{
			name: "byte order mark and unicode",
			src:  "\xef\xbb\xbftags: [i18n]\n\nUtilisez des chaînes traduites. 日本語も.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Tags: []string{"i18n"}, Body: "Utilisez des chaînes traduites. 日本語も."})},
		},
		{
			name: "separator with trailing spaces",
			src:  "One.\n---  \nTwo.\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Body: "One."}),
				rule(1, 3, source.Rule{Body: "Two."}),
			},
		},
		{
			name: "separator inside a code block is body",
			src:  "Front matter looks like this:\n\n```yaml\n---\ntitle: x\n---\n```\n---\nNext.\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Body: "Front matter looks like this:\n\n```yaml\n---\ntitle: x\n---\n```"}),
				rule(1, 9, source.Rule{Body: "Next."}),
			},
		},
		{
			name: "fence closes only on the same character and length",
			src:  "~~~~\n```\n---\n~~~\n---\n~~~~~\n---\nNext.\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Body: "~~~~\n```\n---\n~~~\n---\n~~~~~"}),
				rule(1, 8, source.Rule{Body: "Next."}),
			},
		},
		{
			name: "indented four spaces is not a fence",
			src:  "Code:\n\n    ```\n---\nNext.\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Body: "Code:\n\n    ```"}),
				rule(1, 5, source.Rule{Body: "Next."}),
			},
		},
		{
			name: "lists",
			src:  "tags: db\nscope: ['src/**/*.{go,ts}', \"a, b/*\", c]\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{
				Tags:  []string{"db"},
				Scope: []string{"src/**/*.{go,ts}", "a, b/*", "c"},
				Body:  "Body.",
			})},
		},
		{
			name: "scope exclusions",
			src:  "scope: [\"internal/**/*.go\", \"!**/*_test.go\"]\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{
				Scope: []string{"internal/**/*.go", "!**/*_test.go"},
				Body:  "Body.",
			})},
		},
		{
			name: "a single exclusion without brackets",
			src:  "scope: \"!**/*_test.go\"\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Scope: []string{"!**/*_test.go"}, Body: "Body."})},
		},
		{
			name: "empty list",
			src:  "tags: []\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "Body."})},
		},
		{
			name: "header comment is not a rule",
			src:  "<!--\nstele.md: rules separated by ---\n-->\n\n---\n\nThe first rule.\n",
			want: []source.Rule{rule(0, 7, source.Rule{Body: "The first rule."})},
		},
		{
			name: "comment before metadata is skipped",
			src:  "<!-- the header -->\ntags: [x]\n\nBody.\n",
			want: []source.Rule{rule(0, 2, source.Rule{Tags: []string{"x"}, Body: "Body."})},
		},
		{
			name: "separator inside a comment does not split",
			src:  "<!--\nRules are separated by\n---\non its own line.\n-->\n\nbody one\n",
			want: []source.Rule{rule(0, 7, source.Rule{Body: "body one"})},
		},
		{
			name: "fence inside a comment is not a fence",
			src:  "<!--\n```\n-->\n\nBody.\n",
			want: []source.Rule{rule(0, 5, source.Rule{Body: "Body."})},
		},
		{
			name: "text after a comment on the same line is the rule",
			src:  "<!-- note --> Never do X.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "Never do X."})},
		},
		{
			name: "metadata after a multi-line comment on its closing line",
			src:  "<!--\nheader\n--> tags: [a]\n\nBody.\n",
			want: []source.Rule{rule(0, 3, source.Rule{Tags: []string{"a"}, Body: "Body."})},
		},
		{
			name: "inline code in triple backticks is not a fence",
			src:  "```fmt.Println``` is banned.\n\n---\n\nSecond rule.\n",
			want: []source.Rule{
				rule(0, 1, source.Rule{Body: "```fmt.Println``` is banned."}),
				rule(1, 5, source.Rule{Body: "Second rule."}),
			},
		},
		{
			name: "tilde fence may have backticks in its info string",
			src:  "~~~ `go`\n---\n~~~\n",
			want: []source.Rule{rule(0, 1, source.Rule{Body: "~~~ `go`\n---\n~~~"})},
		},
		{
			name: "apostrophe inside a list item",
			src:  "tags: [don't-care, b]\n\nBody.\n",
			want: []source.Rule{rule(0, 1, source.Rule{Tags: []string{"don't-care", "b"}, Body: "Body."})},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.src))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantMsg string
	}{
		{name: "metadata only", src: "One.\n---\ntags: [a]\n", wantMsg: "stele.md:3: rule has no body"},
		{name: "metadata then blank lines only", src: "tags: [a]\n\n\n", wantMsg: "stele.md:1: rule has no body"},
		{
			name:    "metadata not followed by a blank line",
			src:     "tags: [a]\nNever do this.\n",
			wantMsg: "stele.md:2: metadata must be followed by a blank line",
		},
		{name: "duplicate key", src: "tags: [a]\ntags: [b]\n\nBody.\n", wantMsg: `stele.md:2: duplicate key "tags"`},
		{name: "unclosed list", src: "One.\n---\nscope: [a, b\n\nBody.\n", wantMsg: `stele.md:3: scope: unclosed list`},
		{name: "empty list item", src: "tags: [a,,b]\n\nBody.\n", wantMsg: `stele.md:1: tags: empty list item`},
		{name: "unclosed code block", src: "One.\n---\nSee:\n```go\n---\nTwo.\n", wantMsg: "stele.md:4: unclosed code block"},
		{name: "unclosed comment", src: "<!-- never closed\n\nBody.\n", wantMsg: "stele.md:1: unclosed comment"},
		{
			name:    "unclosed comment swallows separators",
			src:     "One.\n---\n<!-- never closed\n---\nTwo.\n",
			wantMsg: "stele.md:3: unclosed comment",
		},
		{name: "unclosed quote in a list", src: "tags: [\"a, b]\n\nBody.\n", wantMsg: "stele.md:1: tags: unclosed quote"},
		{name: "exclusion with no pattern", src: "One.\n---\nscope: [a, \"!\"]\n\nBody.\n", wantMsg: `stele.md:3: scope: "!" has no pattern after the !`},
		{name: "bare exclusion", src: "scope: !\n\nBody.\n", wantMsg: `stele.md:1: scope: "!" has no pattern after the !`},
		{name: "double exclusion", src: "scope: [\"!!x\"]\n\nBody.\n", wantMsg: `stele.md:1: scope: "!!x": one ! only`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src))
			var pe *source.ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse() error = %v, want a *source.ParseError", err)
			}
			if got := err.Error(); got != tt.wantMsg {
				t.Errorf("Parse() error = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestRead(t *testing.T) {
	ctx := context.Background()
	src := New()
	if got := src.Name(); got != "stelemd" {
		t.Errorf("Name() = %q, want %q", got, "stelemd")
	}

	root := t.TempDir()
	rules, fp, err := src.Read(ctx, root)
	if err != nil || rules != nil || fp != "" {
		t.Errorf("Read() with no stele.md = %v, %q, %v; want no rules, no fingerprint, no error", rules, fp, err)
	}
	fp, err = src.Fingerprint(ctx, root)
	if err != nil || fp != "" {
		t.Errorf("Fingerprint() with no stele.md = %q, %v; want empty, no error", fp, err)
	}

	path := filepath.Join(root, "stele.md")
	if err := os.WriteFile(path, []byte("One.\n---\nTwo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rules, readFP, err := src.Read(ctx, root)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []source.Rule{rule(0, 1, source.Rule{Body: "One."}), rule(1, 3, source.Rule{Body: "Two."})}
	if diff := cmp.Diff(want, rules); diff != "" {
		t.Errorf("Read() mismatch (-want +got):\n%s", diff)
	}
	first, err := src.Fingerprint(ctx, root)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if first == "" || first != readFP {
		t.Errorf("Fingerprint() = %q, Read() fingerprint = %q; want equal and non-empty", first, readFP)
	}
	again, _ := src.Fingerprint(ctx, root)
	if again != first {
		t.Errorf("Fingerprint() of an unchanged file = %q, want %q", again, first)
	}

	if err := os.WriteFile(path, []byte("One, edited.\n---\nTwo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, _ := src.Fingerprint(ctx, root)
	if edited == first {
		t.Errorf("Fingerprint() did not change after an edit")
	}

	if err := os.WriteFile(path, []byte("tags: [a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Read(ctx, root); err == nil {
		t.Errorf("Read() of bad input returned no error")
	}
}

// stele.md is read on every write, so a device or a huge file is refused; a
// symlink to a real file is still followed.
func TestReadRefusesNonFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "stele.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New().Read(context.Background(), root); err == nil {
		t.Error("Read of a directory named stele.md succeeded")
	}

	big := t.TempDir()
	if err := os.WriteFile(filepath.Join(big, "stele.md"), make([]byte, maxSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Fingerprint(context.Background(), big); err == nil {
		t.Error("Fingerprint of an oversized stele.md succeeded")
	}

	linked := t.TempDir()
	real := filepath.Join(t.TempDir(), "rules.md")
	if err := os.WriteFile(real, []byte("A rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(linked, "stele.md")); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	rules, _, err := New().Read(context.Background(), linked)
	if err != nil || len(rules) != 1 {
		t.Errorf("Read through a symlink to a file = %d rules, %v; want 1", len(rules), err)
	}
}
