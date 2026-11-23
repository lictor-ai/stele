package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestRunDispatchesEveryCommand(t *testing.T) {
	want := []string{"check", "compile", "hook", "init", "mcp", "publish", "push", "rules", "stats", "status", "uninstall", "version"}
	var got []string
	for name := range commands {
		got = append(got, name)
	}
	if diff := cmp.Diff(want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("commands mismatch (-want +got):\n%s", diff)
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no args", args: nil, wantCode: exitCantRun, wantStderr: "usage: stele"},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: "usage: stele"},
		{name: "unknown", args: []string{"nope"}, wantCode: exitCantRun, wantStderr: `unknown command "nope"`},
		{name: "stub", args: []string{"mcp"}, wantCode: exitCantRun, wantStderr: "not implemented"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "stele v9.9.9"},
		{name: "--version", args: []string{"--version"}, wantCode: exitOK, wantStdout: "stele v9.9.9"},
		{name: "-v", args: []string{"-v"}, wantCode: exitOK, wantStdout: "stele v9.9.9"},
		{name: "version with an argument", args: []string{"version", "x"}, wantCode: exitCantRun, wantStderr: `unexpected argument "x"`},
		{name: "hook allows silently", args: []string{"hook"}, wantCode: exitOK},
	}
	defer func(v string) { version = v }(version)
	version = "v9.9.9"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if !contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// contains reports whether s contains want, or whether s is empty when want is.
func contains(s, want string) bool {
	if want == "" {
		return s == ""
	}
	return strings.Contains(s, want)
}

// The usage lists every command but the ones not built yet (#157).
func TestUsageHidesUnbuilt(t *testing.T) {
	var stdout bytes.Buffer
	usage(&stdout)
	out := stdout.String()
	if strings.Contains(out, "mcp") {
		t.Errorf("usage lists mcp, which is not built:\n%s", out)
	}
	for name := range commands {
		if !hidden[name] && !strings.Contains(out, "  "+name+" ") {
			t.Errorf("usage does not list %s:\n%s", name, out)
		}
	}
}

// Every command but hook answers -h and --help with its usage on stdout and
// exit 0, and a bad flag with exit 2 (#157). hook reads an agent's payload,
// never a person's flags.
func TestCommandHelp(t *testing.T) {
	for name := range commands {
		if name == "hook" || hidden[name] {
			continue
		}
		for _, flag := range []string{"-h", "--help"} {
			var stdout, stderr bytes.Buffer
			code := run([]string{name, flag}, &stdout, &stderr)
			if code != exitOK || !strings.HasPrefix(stdout.String(), "usage: stele "+name) || stderr.Len() != 0 {
				t.Errorf("stele %s %s = %d, stdout %q, stderr %q; want 0 and the usage on stdout", name, flag, code, stdout.String(), stderr.String())
			}
		}
		var stdout, stderr bytes.Buffer
		code := run([]string{name, "--no-such-flag"}, &stdout, &stderr)
		if code != exitCantRun || !strings.Contains(stderr.String(), "flag provided but not defined") || stdout.Len() != 0 {
			t.Errorf("stele %s --no-such-flag = %d, stdout %q, stderr %q; want 2 and the error on stderr", name, code, stdout.String(), stderr.String())
		}
	}
}
