package check

import "testing"

func TestOwn(t *testing.T) {
	for file, want := range map[string]bool{
		"stele.md":          true,
		"stele.json":        true,
		"stele.lock":        true,
		".stele/index.json": true,
		"internal/a.go":     false,
	} {
		if got := Own(file); got != want {
			t.Errorf("Own(%q) = %v, want %v", file, got, want)
		}
	}
}
