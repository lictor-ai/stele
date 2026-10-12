package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rules = "tags: [db]\n\nNever build SQL by string concatenation.\n\n---\n\nEvery handler goes through the middleware chain.\n"

// project makes a temp repository holding stele.md and enters it.
func project(t *testing.T, steleMD string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if steleMD != "" {
		if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(steleMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	// Never touch the real user's key.
	dir := t.TempDir()
	old := keyDir
	keyDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { keyDir = old })
	return root
}

func runCmd(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCompile(t *testing.T) {
	root := project(t, rules)

	code, out, errOut := runCmd("compile")
	if code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"2 rules: 0 matched by text, 0 by position, 2 new, 0 dropped", "wrote stele.lock and .stele/index.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	for _, f := range []string{"stele.lock", ".stele/index.json", ".stele/.gitignore"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// Run again: nothing to do.
	code, out, _ = runCmd("compile")
	if code != exitOK || !strings.Contains(out, "2 matched by text") || !strings.Contains(out, "up to date, nothing written") {
		t.Errorf("second compile = %d, stdout %q, want up to date", code, out)
	}

	// Reword the second rule: its id is kept by position, and named.
	reworded := strings.Replace(rules, "Every handler goes through", "Every HTTP handler uses", 1)
	if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(reworded), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCmd("compile")
	if code != exitOK || !strings.Contains(out, "1 by position") || !strings.Contains(out, "stelemd rule 2") {
		t.Errorf("compile after reword = %d, stdout %q, want the guess named", code, out)
	}
}

func TestCompileFindsRootFromSubdir(t *testing.T) {
	root := project(t, rules)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if code, _, errOut := runCmd("compile"); code != exitOK {
		t.Fatalf("compile = %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "stele.lock")); err != nil {
		t.Errorf("stele.lock not at the root: %v", err)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		steleMD  string
		compile  bool   // run stele compile first
		after    string // stele.md content written after compiling
		lock     string // stele.lock content written instead of compiling
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "valid", steleMD: rules, compile: true, wantCode: exitOK, wantOut: "2 rules valid"},
		{name: "never compiled", steleMD: rules, wantCode: exitFailed, wantErr: "stele.lock is out of date with the rules: run stele compile"},
		{name: "rules changed since compile", steleMD: rules, compile: true, after: rules + "\n---\n\nA third rule.\n", wantCode: exitFailed, wantErr: "out of date"},
		{name: "parse error", steleMD: "tags: [a]\n", wantCode: exitFailed, wantErr: "stele.md:1: rule has no body"},
		{
			name:    "duplicate id",
			steleMD: rules,
			lock: `{"version": 1, "project": "p", "rules": [
				{"id": "a", "source": "stelemd", "hash": "h1", "position": 0},
				{"id": "a", "source": "stelemd", "hash": "h2", "position": 1}]}`,
			wantCode: exitFailed,
			wantErr:  "duplicate rule id a",
		},
		{name: "invalid lock", steleMD: rules, lock: "{", wantCode: exitFailed, wantErr: "stele.lock: invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, tt.steleMD)
			if tt.compile {
				if code, _, errOut := runCmd("compile"); code != exitOK {
					t.Fatalf("compile = %d, stderr %q", code, errOut)
				}
			}
			if tt.after != "" {
				if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte(tt.after), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.lock != "" {
				if err := os.WriteFile(filepath.Join(root, "stele.lock"), []byte(tt.lock), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, root)

			code, out, errOut := runCmd("check")
			if code != tt.wantCode {
				t.Errorf("check = %d, want %d (stderr %q)", code, tt.wantCode, errOut)
			}
			if !contains(out, tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out, tt.wantOut)
			}
			if tt.wantErr != "" && !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut, tt.wantErr)
			}
			if after := snapshot(t, root); after != before {
				t.Errorf("check wrote files:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestCompileRejectsArguments(t *testing.T) {
	project(t, rules)
	code, _, errOut := runCmd("compile", "extra")
	if code != exitCantRun || !strings.Contains(errOut, `unexpected argument "extra"`) {
		t.Errorf("compile extra = %d, stderr %q", code, errOut)
	}
}

// snapshot lists every file under root with its content.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		b.WriteString(rel + "=" + string(data) + ";")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
