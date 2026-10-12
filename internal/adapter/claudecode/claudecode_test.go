package claudecode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "internal", "db.go")
	cwd := filepath.Dir(filepath.Dir(abs))
	tests := []struct {
		name    string
		payload map[string]any
		want    Event
	}{
		{
			name: "write",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": "p1",
				"tool_name": "Write", "tool_input": map[string]any{"file_path": abs, "content": "package db"},
			},
			want: Event{Name: "PreToolUse", SessionID: "s1", PromptID: "p1", ToolName: "Write", File: abs, NewText: []string{"package db"}},
		},
		{
			name: "edit from a subagent, path relative to cwd",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "s1", "agent_id": "a1", "cwd": cwd,
				"tool_name": "Edit", "tool_input": map[string]any{"file_path": "internal/db.go", "old_string": "x", "new_string": "y"},
			},
			want: Event{Name: "PostToolUse", SessionID: "s1", AgentID: "a1", ToolName: "Edit", File: abs, NewText: []string{"y"}},
		},
		{
			name: "multiedit",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "MultiEdit",
				"tool_input": map[string]any{"file_path": abs, "edits": []map[string]string{{"new_string": "a"}, {"new_string": "b"}}},
			},
			want: Event{Name: "PreToolUse", ToolName: "MultiEdit", File: abs, NewText: []string{"a", "b"}},
		},
		{
			name: "a tool with no file",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
			},
			want: Event{Name: "PreToolUse", ToolName: "Bash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Parse (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"", "{", `{"session_id": "s1"}`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		a    Answer
		want string // "" means no output
	}{
		{name: "nothing to say", a: Answer{}, want: ""},
		{
			name: "deny",
			a:    Answer{Deny: true, Reason: "read the rules"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"read the rules"}}`,
		},
		{
			// Allowing never says "allow": that would skip the user's prompts.
			name: "allow with context",
			a:    Answer{Context: "stele.md has an error"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"stele.md has an error"}}`,
		},
		{
			name: "warning to the user",
			a:    Answer{Warning: "stele could not run"},
			want: `{"systemMessage":"stele could not run"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Render("PreToolUse", tt.a))
			if tt.want != "" {
				tt.want += "\n"
			}
			if got != tt.want {
				t.Errorf("Render =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// PowerShell prepends a UTF-8 byte order mark when it pipes text.
func TestParseByteOrderMark(t *testing.T) {
	in := "\xef\xbb\xbf" + `{"hook_event_name": "PreToolUse", "tool_name": "Write"}`
	got, err := Parse([]byte(in))
	if err != nil || got.Name != "PreToolUse" {
		t.Errorf("Parse with a BOM = %+v, %v", got, err)
	}
}
