package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSaveLoad(t *testing.T) {
	root := t.TempDir()
	want := lockOf([]string{"a", "b"}, md(0, sql), md(1, http))
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

	// Saving again replaces the file and leaves no temp file behind.
	if err := Save(root, want); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("root holds %v, want only %s", names, FileName)
	}
}

func TestSaveEmpty(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, File{Version: 1, Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"rules": []`) {
		t.Errorf("empty lock saved as\n%s\nwant \"rules\": []", data)
	}
}

// stele never writes stele.md (D38): saving the lock leaves it byte for byte.
func TestSaveLeavesSteleMD(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stele.md")
	orig := []byte("tags: [db]\r\n\r\nNever build SQL by string concatenation.\r\n")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, lockOf([]string{"a"}, md(0, sql))); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(orig) {
		t.Errorf("stele.md changed:\n%q\nwant\n%q", got, orig)
	}
}

func TestLoadMissing(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if diff := cmp.Diff(File{}, got); diff != "" {
		t.Errorf("missing lock (-want +got):\n%s", diff)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		is      error
		substr  string
	}{
		{name: "not JSON", content: "{", substr: FileName},
		{name: "unknown version", content: `{"version": 2, "project": "p", "rules": []}`, substr: "version 2"},
		{
			// A bad merge duplicated an entry.
			name: "duplicate id",
			content: `{"version": 1, "project": "p", "rules": [
				{"id": "a", "source": "stelemd", "hash": "h1", "position": 0},
				{"id": "a", "source": "stelemd", "hash": "h2", "position": 1}]}`,
			is:     ErrDuplicateID,
			substr: "rules 1 and 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, FileName), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(root)
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("err = %q, want it to mention %q", err, tt.substr)
			}
		})
	}
}
