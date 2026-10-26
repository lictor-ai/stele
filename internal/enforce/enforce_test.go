package enforce

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" means no file
		want    Policy
		wantErr bool
	}{
		{name: "no file", want: Policy{Backstop: true}},
		{name: "empty object", content: `{}`, want: Policy{Backstop: true}},
		{name: "backstop off", content: `{"backstop": false}`, want: Policy{}},
		{name: "block on error", content: `{"block_on_error": true}`, want: Policy{Backstop: true, BlockOnError: true}},
		{name: "other keys ignored", content: `{"sources": [{"name": "x"}], "backstop": true}`, want: Policy{Backstop: true}},
		{name: "not JSON", content: `{`, want: Policy{Backstop: true}, wantErr: true},
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
