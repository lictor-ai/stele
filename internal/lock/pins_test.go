package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/source"
)

const commitA = "0123456789abcdef0123456789abcdef01234567"

func pinned() File {
	f := lockOf([]string{"a"}, md(0, sql))
	f.Sources = []source.Pin{{Source: "git:github.com/acme/g/stele.md", Repo: "git@github.com:acme/g.git", Ref: "main", Path: "stele.md", Commit: commitA}}
	return f
}

// A lock with pins is version 2, so an older stele refuses it rather than
// drop the pins at its next compile; one without stays version 1.
func TestSaveVersionFollowsPins(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, pinned()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, FileName))
	if !strings.Contains(string(data), `"version": 2`) || !strings.Contains(string(data), `"commit": "`+commitA+`"`) {
		t.Errorf("lock with pins =\n%s\nwant version 2 and the commit", data)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	want := pinned()
	want.Version = 2
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}

	if err := Save(root, lockOf([]string{"a"}, md(0, sql))); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, FileName))
	if !strings.Contains(string(data), `"version": 1`) || strings.Contains(string(data), "sources") {
		t.Errorf("lock without pins =\n%s\nwant version 1 and no sources", data)
	}
}

func TestEqualSeesPins(t *testing.T) {
	a, b := pinned(), pinned()
	if !Equal(a, b) {
		t.Fatal("same pins are not Equal")
	}
	b.Sources[0].Commit = strings.Repeat("f", 40)
	if Equal(a, b) {
		t.Error("a moved pin is Equal")
	}
}

func TestValidatePins(t *testing.T) {
	for name, edit := range map[string]func(*File){
		"no source":  func(f *File) { f.Sources[0].Source = "" },
		"bad commit": func(f *File) { f.Sources[0].Commit = "HEAD" },
		"short":      func(f *File) { f.Sources[0].Commit = "0123abc" },
		"twice":      func(f *File) { f.Sources = append(f.Sources, f.Sources[0]) },
	} {
		f := pinned()
		edit(&f)
		if err := Validate(f); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
	f := pinned()
	f.Sources[0].Commit = strings.Repeat("a", 64) // a SHA-256 repository
	if err := Validate(f); err != nil {
		t.Errorf("64-hex commit: %v", err)
	}
}

func TestLoadAcceptsVersions(t *testing.T) {
	for _, v := range []string{"1", "2"} {
		root := t.TempDir()
		data := `{"version": ` + v + `, "project": "p", "rules": []}`
		if err := os.WriteFile(filepath.Join(root, FileName), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err != nil {
			t.Errorf("version %s: %v", v, err)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(`{"version": 3, "project": "p", "rules": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); !errors.Is(err, ErrInvalid) {
		t.Errorf("version 3: %v, want ErrInvalid", err)
	}
}

func TestDecode(t *testing.T) {
	f, err := Decode([]byte(`{"version": 1, "project": "p", "rules": [{"id": "x", "source": "stelemd", "hash": "h", "position": 0}]}`))
	if err != nil || len(f.Rules) != 1 || f.Rules[0].ID != "x" {
		t.Errorf("Decode = %+v, %v", f, err)
	}
	if _, err := Decode([]byte(`{nope`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("Decode of bad JSON = %v, want ErrInvalid", err)
	}
	if _, err := Decode([]byte(`{"version": 1, "rules": []}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("Decode with no project = %v, want ErrInvalid", err)
	}
}
