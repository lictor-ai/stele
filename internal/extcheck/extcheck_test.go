package extcheck

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lictor-ai/stele/internal/plugin"
)

func TestParseList(t *testing.T) {
	for in, want := range map[string][]string{
		"":                nil,
		"[]":              nil,
		"vuln":            {"vuln"},
		`"vuln"`:          {"vuln"},
		"[vuln, secret]":  {"vuln", "secret"},
		`["a,b", 'c']`:    {"a,b", "c"},
		"[ python-sqli ]": {"python-sqli"},
		"[don't-care, x]": {"don't-care", "x"},
	} {
		got, err := ParseList(in)
		if err != nil {
			t.Errorf("ParseList(%q): %v", in, err)
			continue
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ParseList(%q) (-want +got):\n%s", in, diff)
		}
	}
	for _, in := range []string{"[a", "[a,,b]", `["a]`} {
		if _, err := ParseList(in); err == nil {
			t.Errorf("ParseList(%q) = nil error", in)
		}
	}
}

func TestProjectFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r", "a.yml"), []byte("rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ProjectFile(dir, "r/a.yml")
	if err != nil || filepath.ToSlash(got) != filepath.ToSlash(filepath.Join(dir, "r", "a.yml")) {
		t.Errorf("ProjectFile = %q, %v", got, err)
	}
	for rel, want := range map[string]string{
		"":              "is empty",
		"/r/a.yml":      "is absolute",
		`\r\a.yml`:      "is absolute",
		"C:/r/a.yml":    "",
		"../a.yml":      `holds ".."`,
		`r\..\..\a.yml`: `holds ".."`,
		"r/nope.yml":    "no such file",
		"r":             "is not a file",
	} {
		_, err := ProjectFile(dir, rel)
		if err == nil || want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("ProjectFile(%q) = %v, want an error with %q", rel, err, want)
		}
	}
	if _, err := ProjectFile(dir, "../x"); !errors.Is(err, ErrOutside) {
		t.Errorf("ProjectFile(../x) = %v, want ErrOutside", err)
	}
}

func TestFilesLookup(t *testing.T) {
	dir := t.TempDir()
	fs := NewFiles(dir, []string{"app/db.py", "Dockerfile"})
	for p, want := range map[string]string{
		"app/db.py":                        "app/db.py",
		`app\db.py`:                        "app/db.py",
		filepath.Join(dir, "app", "db.py"): "app/db.py",
		filepath.ToSlash(filepath.Join(dir, "Dockerfile")): "Dockerfile",
		"./Dockerfile": "Dockerfile",
		"other.py":     "",
		filepath.Join(t.TempDir(), "app", "db.py"): "",
	} {
		got, ok := fs.Lookup(p)
		if got != want || ok != (want != "") {
			t.Errorf("Lookup(%q) = %q, %v; want %q", p, got, ok, want)
		}
	}
}

func TestColumn(t *testing.T) {
	for _, c := range []struct {
		line string
		off  int
		want int
	}{
		{"abc", 0, 1}, {"abc", 2, 3}, {`x = "é"; q`, 10, 10}, {"abc", 99, 4}, {"\tx", 1, 2},
	} {
		if got := Column(c.line, c.off); got != c.want {
			t.Errorf("Column(%q, %d) = %d, want %d", c.line, c.off, got, c.want)
		}
	}
}

type stub struct{ err error }

func (s stub) Validate(context.Context, Request) ([]Problem, error) { return nil, s.err }
func (s stub) Check(context.Context, Request) ([]Violation, []Skip, error) {
	return nil, nil, s.err
}

// Every answer is one protocol 1 object stele can decode: no problems and
// no violations as [], an error as {"error": …}.
func TestServe(t *testing.T) {
	dir := t.TempDir()
	quoted := strings.ReplaceAll(dir, `\`, `\\`)
	for _, c := range []struct {
		name, req string
		h         Handler
		want      string // in the answer
		wantErr   string // from DecodeAnswer
	}{
		{"validate", `{"protocol":1,"op":"validate","project_dir":"` + quoted + `","rules":[]}`, stub{}, `"problems":[]`, ""},
		{"check", `{"protocol":1,"op":"check","project_dir":"` + quoted + `","files":[],"rules":[]}`, stub{}, `"violations":[]`, ""},
		{"old root", `{"protocol":1,"op":"check","root":"` + quoted + `","files":[],"rules":[]}`, stub{}, `"skipped":[]`, ""},
		{"handler error", `{"protocol":1,"op":"check","project_dir":"` + quoted + `"}`, stub{err: errors.New("opengrep: not found on PATH")}, "", "opengrep: not found on PATH"},
		{"no dir", `{"protocol":1,"op":"check"}`, stub{}, "", "no absolute project_dir"},
		{"protocol 2", `{"protocol":2,"op":"describe"}`, stub{}, "", "unsupported protocol 2"},
		{"unknown op", `{"protocol":1,"op":"read"}`, stub{}, "", `unknown op "read"`},
		{"not JSON", `nope`, stub{}, "", "bad request"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := Serve(context.Background(), "v1", c.h, strings.NewReader(c.req), &out); code != 0 {
				t.Fatalf("Serve = %d", code)
			}
			var v map[string]any
			err := plugin.DecodeAnswer(out.Bytes(), &v)
			switch {
			case c.wantErr == "" && err != nil:
				t.Errorf("answer %s: %v", out.String(), err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Errorf("answer %s: %v, want %q", out.String(), err, c.wantErr)
			case !strings.Contains(out.String(), c.want):
				t.Errorf("answer %s, want %s in it", out.String(), c.want)
			}
		})
	}
}

func TestKnownKeys(t *testing.T) {
	ps := KnownKeys(Rule{ID: "r", Meta: map[string]string{"check": "x", "blocking": "false", "rules": "a", "zz": "1", "aa": "2"}}, "rules")
	if len(ps) != 2 || !strings.HasPrefix(ps[0].Message, "aa:") || !strings.HasPrefix(ps[1].Message, "zz:") {
		t.Errorf("KnownKeys = %+v", ps)
	}
}
