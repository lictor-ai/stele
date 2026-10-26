package publish

import "testing"

func TestPrefilledPage(t *testing.T) {
	tests := []struct {
		repo, base, branch, want string
	}{
		{
			"git@github.com:me/g.git", "main", "stele/publish/a/stele.md",
			"https://github.com/me/g/compare/main...stele/publish/a/stele.md?expand=1",
		},
		{
			"git@github.com:me/g.git", "rel/a b#%", "stele/publish/stele.md",
			"https://github.com/me/g/compare/rel/a%20b%23%25...stele/publish/stele.md?expand=1",
		},
		{
			"https://gitlab.com/a/b/c.git", "main", "stele/publish/a/stele.md",
			"https://gitlab.com/a/b/c/-/merge_requests/new?merge_request%5Bsource_branch%5D=stele%2Fpublish%2Fa%2Fstele.md",
		},
		{"https://git.example.com/a/b.git", "main", "stele/publish/stele.md", ""},
		{"https://github.com/me/g/extra", "main", "stele/publish/stele.md", ""},
	}
	for _, tt := range tests {
		if got := prefilledPage(parseRemote(tt.repo), tt.base, tt.branch); got != tt.want {
			t.Errorf("prefilledPage(%q, %q) =\n  %s\nwant\n  %s", tt.repo, tt.base, got, tt.want)
		}
	}
}

func TestSameHostHTTPS(t *testing.T) {
	tests := []struct {
		link string
		want bool
	}{
		{"https://github.com/me/g/pull/3", true},
		{"http://github.com/me/g/pull/3", false},
		{"https://github.com.evil/me/g/pull/3", false},
		{"https://evil/x", false},
		{"https://user@github.com/x", false},
		{"https://github.com:8443/x", false},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
	}
	for _, tt := range tests {
		if got := sameHostHTTPS(tt.link, "github.com"); got != tt.want {
			t.Errorf("sameHostHTTPS(%q) = %v, want %v", tt.link, got, tt.want)
		}
	}
}

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "red"},
		{"a\rb", "ab"},
		{"x‮gnp.exe", "xgnp.exe"},
		{"line1\nline2\tx", "line1\nline2\tx"},
		{"bell\a", "bell"},
	}
	for _, tt := range tests {
		if got := Clean(tt.in); got != tt.want {
			t.Errorf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
