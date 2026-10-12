package index

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func sample() Index {
	return Index{
		Version: Version,
		Project: "proj",
		Sources: map[string]string{"stelemd": "fp"},
		Rules: []Entry{{
			ID:       "a",
			Source:   "stelemd",
			Position: 0,
			Tags:     []string{"db"},
			Meta:     map[string]string{"check": "regex"},
			Body:     "Never build SQL by string concatenation.",
			BodyHash: "h",
		}},
	}
}

func TestSaveLoad(t *testing.T) {
	root := t.TempDir()
	want := sample()
	if err := Save(root, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}

	// .stele/ is ignored by its own .gitignore.
	ignore, err := os.ReadFile(filepath.Join(root, Dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "*\n" {
		t.Errorf(".gitignore = %q, want %q", ignore, "*\n")
	}
}

// A .gitignore that is already there is left alone.
func TestSaveKeepsGitignore(t *testing.T) {
	root := t.TempDir()
	ignore := filepath.Join(root, Dir, ".gitignore")
	if err := os.MkdirAll(filepath.Dir(ignore), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignore, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, sample()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "custom\n" {
		t.Errorf(".gitignore = %q, want it unchanged", got)
	}
}

func TestLoadNoIndex(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" means no file
	}{
		{name: "missing"},
		{name: "not JSON", content: "{"},
		{name: "other version", content: `{"version": 2, "project": "p", "sources": {}, "rules": []}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != "" {
				if err := os.MkdirAll(filepath.Join(root, Dir), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(Path(root), []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(root); !errors.Is(err, ErrNoIndex) {
				t.Errorf("Load: err = %v, want ErrNoIndex", err)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	a := sample()
	if !Equal(a, sample()) {
		t.Error("identical indexes differ")
	}
	empty := Index{Project: "p"}
	if !Equal(empty, Index{Project: "p", Sources: map[string]string{}, Rules: []Entry{}}) {
		t.Error("nil and empty fields differ, want equal: both save alike")
	}
	b := sample()
	b.Sources = map[string]string{"stelemd": "changed"}
	if Equal(a, b) {
		t.Error("indexes with different fingerprints are equal")
	}
}
