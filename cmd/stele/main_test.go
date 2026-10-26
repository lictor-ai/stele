package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestRunDispatchesEveryCommand(t *testing.T) {
	want := []string{"check", "compile", "hook", "init", "mcp", "publish", "push", "status", "uninstall"}
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
		{name: "stub", args: []string{"status"}, wantCode: exitCantRun, wantStderr: "not implemented"},
		{name: "hook allows silently", args: []string{"hook"}, wantCode: exitOK},
	}
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
