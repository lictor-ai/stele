package enforce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" means no file
		want    Policy
		wantErr bool
	}{
		{name: "no file", want: Policy{}},
		{name: "empty object", content: `{}`, want: Policy{}},
		{name: "backstop is no longer read", content: `{"backstop": false}`, want: Policy{}},
		{name: "block on error", content: `{"block_on_error": true}`, want: Policy{BlockOnError: true}},
		{name: "other keys ignored", content: `{"sources": [{"name": "x"}], "backstop": true}`, want: Policy{}},
		{name: "not JSON", content: `{`, want: Policy{}, wantErr: true},
		{name: "byte order mark", content: "\xef\xbb\xbf" + `{"block_on_error": true}`, want: Policy{BlockOnError: true}},
		{name: "at the cap", content: `{"block_on_error": true}` + strings.Repeat(" ", maxSize-24), want: Policy{BlockOnError: true}},
		{name: "over the cap", content: `{"block_on_error": true}` + strings.Repeat(" ", maxSize), want: Policy{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != "" {
				if err := os.WriteFile(filepath.Join(root, FileName), []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Load(root)
			if (err != nil) != tt.wantErr {
				t.Errorf("Load: err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Load = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A link is followed, but it must end at a regular file: a repository could
// commit stele.json as a link to /dev/zero, and the hook reads it on every
// write.
func TestLoadFileTypes(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, FileName), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, err := Load(root); err == nil || got != Default() {
			t.Errorf("Load = %+v, %v; want the default policy and an error", got, err)
		}
		if SetsBackstop(root) {
			t.Error("SetsBackstop = true on a directory")
		}
	})
	t.Run("link to a regular file", func(t *testing.T) {
		root := t.TempDir()
		shared := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(shared, []byte(`{"block_on_error": true, "backstop": true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		link(t, shared, filepath.Join(root, FileName))
		if got, err := Load(root); err != nil || !got.BlockOnError {
			t.Errorf("Load = %+v, %v; want block_on_error from the linked file", got, err)
		}
		if !SetsBackstop(root) {
			t.Error("SetsBackstop = false through a link")
		}
	})
	t.Run("link to a directory", func(t *testing.T) {
		root := t.TempDir()
		link(t, t.TempDir(), filepath.Join(root, FileName))
		if got, err := Load(root); err == nil || got != Default() {
			t.Errorf("Load = %+v, %v; want the default policy and an error", got, err)
		}
	})
	t.Run("dangling link", func(t *testing.T) {
		root := t.TempDir()
		link(t, filepath.Join(root, "gone"), filepath.Join(root, FileName))
		if got, err := Load(root); err != nil || got != Default() {
			t.Errorf("Load = %+v, %v; want the default policy, no error", got, err)
		}
	})
}

// link makes a symlink, or skips where the system refuses one.
func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

func TestSetsBackstop(t *testing.T) {
	tests := []struct {
		content string // "" means no file
		want    bool
	}{
		{"", false},
		{`{}`, false},
		{`{"backstop": false}`, true},
		{`{"backstop": true}`, true},
		{`{"block_on_error": true}`, false},
		{`{`, false},
		{"\xef\xbb\xbf" + `{"backstop": true}`, true},
		{`{"backstop": true}` + strings.Repeat(" ", maxSize), false},
	}
	for _, tt := range tests {
		root := t.TempDir()
		if tt.content != "" {
			if err := os.WriteFile(filepath.Join(root, FileName), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := SetsBackstop(root); got != tt.want {
			t.Errorf("SetsBackstop(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}
