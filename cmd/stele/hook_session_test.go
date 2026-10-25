package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionPayload returns the recorded SessionStart payload in
// testdata/hook/session-start.json for event, with Claude Code running in cwd.
func sessionPayload(t *testing.T, event, cwd string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, "hook", "session-start.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	payload["hook_event_name"], payload["cwd"] = event, cwd
	return payload
}

// At the start of a session or subagent in a stele project, the agent is
// told to write files with Write and Edit: a write through Bash skips stele
// (#50).
func TestHookSessionStart(t *testing.T) {
	root := project(t, hookRules)
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		t.Run(event, func(t *testing.T) {
			for _, cwd := range []string{root, filepath.Join(root, "internal")} {
				o, _ := hook(t, sessionPayload(t, event, cwd))
				ctx := o.HookSpecificOutput.AdditionalContext
				if !strings.Contains(ctx, "Write and Edit") || !strings.Contains(ctx, "Bash") {
					t.Errorf("cwd %s: context = %q, want the Write and Edit guidance", cwd, ctx)
				}
				if o.HookSpecificOutput.PermissionDecision != "" || o.Decision != "" || o.SystemMessage != "" {
					t.Errorf("cwd %s: answer = %+v, want context only", cwd, o)
				}
			}
		})
	}
}

func TestHookSessionStartOutsideStele(t *testing.T) {
	plain := t.TempDir()
	if err := os.Mkdir(filepath.Join(plain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{plain, ""} {
		if _, out := hook(t, sessionPayload(t, "SessionStart", cwd)); out != "" {
			t.Errorf("cwd %q: stdout = %q, want nothing outside a stele project", cwd, out)
		}
	}
}
