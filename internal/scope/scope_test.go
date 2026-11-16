package scope

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
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

func TestAppliesExclusions(t *testing.T) {
	caseSensitive(t)
	goNoTests := []string{"internal/**/*.go", "!**/*_test.go"}
	tests := []struct {
		patterns []string
		file     string
		want     bool
	}{
		{goNoTests, "internal/a/x.go", true},
		{goNoTests, "internal/a/x_test.go", false},
		{goNoTests, "cmd/x.go", false},
		// Order does not matter.
		{[]string{"!**/*_test.go", "internal/**/*.go"}, "internal/a/x.go", true},
		{[]string{"!**/*_test.go", "internal/**/*.go"}, "internal/a/x_test.go", false},
		// Only exclusions: every other file.
		{[]string{"!docs/"}, "a.go", true},
		{[]string{"!docs/"}, "docs/x.md", false},
		// Several exclusions, braces, anchors.
		{[]string{"**/*.go", "!**/*_{test,mock}.go", "!/vendor/"}, "a_mock.go", false},
		{[]string{"**/*.go", "!**/*_{test,mock}.go", "!/vendor/"}, "vendor/a.go", false},
		{[]string{"**/*.go", "!**/*_{test,mock}.go", "!/vendor/"}, "x/vendor/a.go", true},
		// A broken or over-cap exclusion excludes nothing: it never hides a rule.
		{[]string{"**/*.go", `!["`}, "a.go", true},
		{[]string{"**/*.go", "!" + strings.Repeat("a", maxPattern) + "*"}, strings.Repeat("a", maxPattern) + "x.go", true},
		// A literal ! file is matched by escaping it.
		{[]string{`\!x`}, "!x", true},
		{nil, "a.go", true},
	}
	for _, tt := range tests {
		if got := Applies(tt.patterns, tt.file); got != tt.want {
			t.Errorf("Applies(%q, %q) = %v, want %v", tt.patterns, tt.file, got, tt.want)
		}
	}
}

func TestAppliesExclusionFoldsCase(t *testing.T) {
	old := foldCase
	t.Cleanup(func() { foldCase = old })
	foldCase = true
	if Applies([]string{"**/*.go", "!**/*_TEST.go"}, "a/x_test.go") {
		t.Error("with case folding, !**/*_TEST.go should exclude a/x_test.go")
	}
}

func TestValidate(t *testing.T) {
	for _, ok := range [][]string{nil, {"*.go", "docs/"}, {"!x", "*.go"}, {`\!x`}} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"!", "! x", "!  ", "!!x"} {
		err := Validate([]string{"*.go", bad})
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(bad)) {
			t.Errorf("Validate(%q) = %v, want an error naming it", bad, err)
		}
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
// pattern may make matching slow. The work is counted, not timed, as a clock
// measures the machine's load as well (#138): a pattern over maxPattern is
// refused before any work, brace expansion stops at maxAlternatives, and each
// (pattern, path) position is computed once.
func TestMatchBounded(t *testing.T) {
	caseSensitive(t)
	deep := strings.TrimSuffix(strings.Repeat("a/", 40), "/")

	for _, p := range []string{
		strings.Repeat("{", 80000) + strings.Repeat("}", 80000),           // deep nesting
		strings.Repeat("{a,b}", 8) + strings.Repeat("x", 500000),          // long literal × 256
		"{" + strings.Repeat(strings.Repeat("{a,b}", 8)+",", 20000) + "}", // many wide branches
		strings.Repeat("{a,b}", 8) + strings.Repeat("/**", 20000) + "/z",  // long ** run × 256
	} {
		if allocs := testing.AllocsPerRun(1, func() { Match(p, deep) }); allocs != 0 {
			t.Errorf("Match(%.30q...), %d bytes, allocated %v times, want none", p, len(p), allocs)
		}
	}

	// Smallest first: expanded in full, {a,b}×18 allocates some 20 MiB, and
	// the test stops there, before {a,b}×30 runs out of memory. Within the
	// caps the most these take is about 0.65 MiB, under a budget of 2 MiB.
	const budget = 8 * maxPattern * maxAlternatives
	for _, p := range []string{
		strings.Repeat("{a,b}", 9),                                                     // brace bomb
		strings.Repeat("{a,b}", 18),                                                    // brace bomb
		strings.Repeat("{a,b}", 30),                                                    // brace bomb
		strings.Repeat("{a,b}", 8) + strings.Repeat("x", maxPattern-40),                // long literal × 256
		"{" + strings.Repeat(strings.Repeat("{a,b}", 8)+",", (maxPattern-2)/41) + "}",  // many wide branches
		strings.Repeat("{a}", maxPattern/3),                                            // many groups
		"{" + strings.Repeat("x,", 200) + "x}/" + strings.Repeat("**/a/", 10) + "b",    // wide, then backtracking
		strings.Repeat("{a,b}", 8) + strings.Repeat("/**", (maxPattern-40-2)/3) + "/z", // long ** run × 256
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		expand(p)
		runtime.ReadMemStats(&after)
		if got := after.TotalAlloc - before.TotalAlloc; got > budget {
			t.Fatalf("expanding %.30q..., %d bytes, allocated %d bytes, budget %d", p, len(p), got, budget)
		}
	}

	// Smallest first: without memoisation **/a/×5 computes 2.4 million
	// positions, and the test stops there, before **/a/×20 runs for ever.
	file := strings.Split(deep, "/")
	for _, p := range []string{
		strings.Repeat("**/a/", 5) + "b",  // backtracking bomb
		strings.Repeat("**/a/", 20) + "b", // backtracking bomb
		"{" + strings.Repeat("x,", 200) + "x}/" + strings.Repeat("**/a/", 10) + "b",
		strings.Repeat("{a,b}", 8) + strings.Repeat("/**", (maxPattern-40-2)/3) + "/z",
	} {
		alts, ok := expand(p)
		if !ok {
			t.Fatalf("%.30q... refused", p)
		}
		for _, alt := range alts {
			pat := strings.Split(alt, "/")
			if _, got := segments(pat, file); got > (len(pat)+1)*(len(file)+1) {
				t.Fatalf("%.30q... computed %d positions of %d", alt, got, (len(pat)+1)*(len(file)+1))
			}
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
