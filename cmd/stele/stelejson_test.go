package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trailingComma is a stele.json with a syntax error at line 3, column 1.
const trailingComma = "{\n  \"block_on_error\": false,\n}\n"

// trailingCommaError is how every command reports it.
const trailingCommaError = "stele.json: invalid JSON at line 3, column 1: invalid character '}' looking for beginning of object key string"

func writeSteleJSON(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A syntax error in stele.json is reported once, as a syntax error at its
// line and column, by every command that reads the file: not as bad sources,
// checkers or sinks (#150).
func TestSteleJSONSyntaxError(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		code int
	}{
		{"compile", exitCantRun},
		{"check", exitCantRun},
		{"status", exitCantRun},
		{"push", exitCantRun},
		{"publish", 1},
	} {
		t.Run(c.cmd, func(t *testing.T) {
			root := project(t, rules)
			writeSteleJSON(t, root, trailingComma)
			code, out, errOut := runCmd(c.cmd)
			want := "stele " + c.cmd + ": " + trailingCommaError + "\n"
			if code != c.code || errOut != want {
				t.Errorf("%s = %d, stdout %q, stderr %q; want %d and stderr %q", c.cmd, code, out, errOut, c.code, want)
			}
		})
	}
	t.Run("init", func(t *testing.T) {
		root := project(t, rules)
		writeSteleJSON(t, root, trailingComma)
		code, out, errOut := runCmd("init")
		if code != exitOK {
			t.Fatalf("init = %d, stdout %q, stderr %q", code, out, errOut)
		}
		want := "stele init: " + trailingCommaError + ": fix it and run stele compile to write .claude/rules/stele-rules.md\n"
		if !strings.Contains(errOut, want) || strings.Count(errOut, "stele.json") != 1 {
			t.Errorf("stderr = %q, want it to hold once %q", errOut, want)
		}
	})
	// stele rules reads the compiled index, not stele.json: it still lists
	// the rules.
	t.Run("rules", func(t *testing.T) {
		root := project(t, rules)
		if code, _, errOut := runCmd("compile"); code != exitOK {
			t.Fatalf("compile: %s", errOut)
		}
		writeSteleJSON(t, root, trailingComma)
		code, out, errOut := runCmd("rules")
		if code != exitOK || !strings.Contains(out, "Never build SQL") || errOut != "" {
			t.Errorf("rules = %d, stdout %q, stderr %q", code, out, errOut)
		}
	})
	// Valid JSON with a bad entry is still a bad entry.
	t.Run("a bad entry", func(t *testing.T) {
		root := project(t, rules)
		writeSteleJSON(t, root, `{"sources": [{"name": "Bad"}]}`)
		code, _, errOut := runCmd("compile")
		if code != exitCantRun || !strings.HasPrefix(errOut, "stele compile: bad sources in stele.json: sources[0]: ") {
			t.Errorf("compile = %d, stderr %q", code, errOut)
		}
	})
}

// After a write, a stele.json with a syntax error leaves the rules in force:
// the write is still checked against them, and the user is told where the
// error is (#150).
func TestHookPostChecksDespiteSteleJSONSyntaxError(t *testing.T) {
	const regexRules = "scope: [\"**/*.go\"]\ncheck: regex\npattern: fmt\\.Print\n\nNo printing.\n"
	clock(t, time.Now())
	root := project(t, regexRules)
	writeSteleJSON(t, root, trailingComma)
	o, _ := postHook(t, root, "post-write", "a.go", "package a\n\nfunc f() { fmt.Println() }\n")
	if o.Decision != "block" {
		t.Errorf("decision %q, want block: the rules stopped being checked", o.Decision)
	}
	want := "stele: " + trailingCommaError + ". The rules still apply; block_on_error is off until stele.json is fixed."
	if o.SystemMessage != want {
		t.Errorf("warning = %q, want %q", o.SystemMessage, want)
	}
}
