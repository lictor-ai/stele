package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRoot(t *testing.T) {
	// repo/.git, repo/sub/stele.md, repo/sub/deep/, repo/other/
	repo := t.TempDir()
	for _, d := range []string{".git", "sub/deep", "other"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "stele.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, from, want string
	}{
		{name: "stele.md wins over .git", from: "sub/deep", want: "sub"},
		{name: "stele.md in the directory itself", from: "sub", want: "sub"},
		{name: "falls back to .git", from: "other", want: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findRoot(filepath.Join(repo, tt.from))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(repo, tt.want); got != want {
				t.Errorf("findRoot(%s) = %s, want %s", tt.from, got, want)
			}
		})
	}
}
