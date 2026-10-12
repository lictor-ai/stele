package scope

import "testing"

func TestMatch(t *testing.T) {
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

// Brace expansion is capped, so a hostile pattern stays cheap.
func TestExpandCapped(t *testing.T) {
	p := ""
	for range 20 {
		p += "{a,b}"
	}
	if n := len(expand(p)); n > maxAlternatives {
		t.Errorf("expanded to %d alternatives, want at most %d", n, maxAlternatives)
	}
}
