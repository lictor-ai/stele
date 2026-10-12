package scope

import (
	"strings"
	"testing"
	"time"
)

// caseSensitive runs a test with case folding off, so results do not depend on
// the platform.
func caseSensitive(t *testing.T) {
	t.Helper()
	old := foldCase
	foldCase = false
	t.Cleanup(func() { foldCase = old })
}

func TestMatch(t *testing.T) {
	caseSensitive(t)
	tests := []struct {
		pattern, file string
		want          bool
	}{
		// ** spans directories, including none.
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/db/query.go", true},
		{"**/*.go", "internal/db/query.ts", false},
		{"internal/**", "internal/db/query.go", true},
		{"internal/**", "cmd/main.go", false},
		{"internal/**/query.go", "internal/query.go", true},
		{"internal/**/query.go", "internal/a/b/query.go", true},
		// One segment for *.
		{"internal/*.go", "internal/x.go", true},
		{"internal/*.go", "internal/db/x.go", false},
		// No slash: the name at any depth.
		{"*.go", "a/b/c.go", true},
		{"Makefile", "tools/Makefile", true},
		{"*.go", "a/b/c.goo", false},
		// Braces, nested braces.
		{"src/**/*.{go,ts}", "src/a/b.ts", true},
		{"src/**/*.{go,ts}", "src/a/b.js", false},
		{"{cmd,internal}/**/*.go", "cmd/stele/main.go", true},
		{"a/{b,c{d,e}}/f", "a/cd/f", true},
		{"a/{b,c{d,e}}/f", "a/c/f", false},
		// Directories, anchors.
		{"docs/", "docs/adr/0001.md", true},
		{"docs/", "src/docs.go", false},
		{"/cmd/**", "cmd/x.go", true},
		{"./cmd/**", "cmd/x.go", true},
		// A leading / or ./ anchors at the root, even with no other slash.
		{"/*.go", "main.go", true},
		{"/*.go", "sub/x.go", false},
		{"./*.go", "sub/x.go", false},
		{"/Makefile", "sub/Makefile", false},
		// Classes and ?.
		{"v[0-9].txt", "v1.txt", true},
		{"v?.txt", "v12.txt", false},
		// Malformed patterns match nothing.
		{"[", "[", false},
		{"", "a.go", false},
		// Paths are cleaned first.
		{"internal/**", "./internal/x.go", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.file); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
		}
	}
}

func TestApplies(t *testing.T) {
	if !Applies(nil, "anything.md") {
		t.Error("a rule with no scope must apply everywhere")
	}
	if !Applies([]string{"*.ts", "*.go"}, "a/b.go") {
		t.Error("any matching pattern should do")
	}
	if Applies([]string{"*.ts"}, "a/b.go") {
		t.Error("no pattern matches")
	}
}

// A pattern expanding to too many alternatives matches nothing.
func TestExpandCapped(t *testing.T) {
	if _, ok := expand(strings.Repeat("{a,b}", 9)); ok {
		t.Error("512 alternatives accepted, want the pattern refused")
	}
	if alts, ok := expand(strings.Repeat("{a,b}", 8)); !ok || len(alts) != 256 {
		t.Errorf("256 alternatives: %d, %v", len(alts), ok)
	}
}

// stele.md comes from the repository and the hook runs on every write, so no
// pattern may make matching slow.
func TestMatchStaysFast(t *testing.T) {
	deep := strings.TrimSuffix(strings.Repeat("a/", 40), "/")
	patterns := []string{
		strings.Repeat("{a,b}", 30),       // brace bomb
		strings.Repeat("**/a/", 20) + "b", // backtracking bomb
		"{" + strings.Repeat("x,", 200) + "x}/" + strings.Repeat("**/a/", 10) + "b",
		strings.Repeat("{", 80000) + strings.Repeat("}", 80000),           // deep nesting
		strings.Repeat("{a,b}", 8) + strings.Repeat("x", 500000),          // long literal × 256
		"{" + strings.Repeat(strings.Repeat("{a,b}", 8)+",", 20000) + "}", // many wide branches
		strings.Repeat("{a,b}", 8) + strings.Repeat("/**", 20000) + "/z",  // long ** run × 256
	}
	for _, p := range patterns {
		start := time.Now()
		Match(p, deep)
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("Match(%.30q...) took %v", p, d)
		}
	}
}

func TestFoldCase(t *testing.T) {
	old := foldCase
	t.Cleanup(func() { foldCase = old })

	foldCase = true
	if !Match("internal/**", "Internal/X.go") {
		t.Error("with case folding, Internal/X.go should match internal/**")
	}
	foldCase = false
	if Match("internal/**", "Internal/X.go") {
		t.Error("without case folding, Internal/X.go should not match internal/**")
	}
}

func TestLongPatternMatchesNothing(t *testing.T) {
	long := strings.Repeat("a", maxPattern) + "*"
	if Match(long, strings.Repeat("a", maxPattern)+"x") {
		t.Error("a pattern over the length cap matched")
	}
}
