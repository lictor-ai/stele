package claudecode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/adapter"
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
			want: Event{Kind: adapter.PreTool, Writes: true, Whole: true, Name: "PreToolUse", SessionID: "s1", PromptID: "p1", ToolName: "Write", File: abs, NewText: []string{"package db"}},
		},
		{
			name: "edit from a subagent, path relative to cwd",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "s1", "agent_id": "a1", "cwd": cwd,
				"tool_name": "Edit", "tool_input": map[string]any{"file_path": "internal/db.go", "old_string": "x", "new_string": "y"},
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", SessionID: "s1", AgentID: "a1", Cwd: cwd, ToolName: "Edit", File: abs, NewText: []string{"y"}, ReplaceAll: []bool{false}},
		},
		{
			name: "bash",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": cwd, "tool_name": "Bash", "tool_use_id": "toolu_2",
				"tool_input": map[string]any{"command": "cat > a.go <<EOF\nx\nEOF", "description": "write a.go", "run_in_background": true},
			},
			want: Event{Kind: adapter.PreShell, Name: "PreToolUse", SessionID: "s1", Cwd: cwd, ToolName: "Bash", Command: "cat > a.go <<EOF\nx\nEOF", Background: true},
		},
		{
			name: "multiedit",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "MultiEdit",
				"tool_input": map[string]any{"file_path": abs, "edits": []map[string]any{{"new_string": "a"}, {"new_string": "b", "replace_all": true}}},
			},
			want: Event{Kind: adapter.PreTool, Writes: true, Name: "PreToolUse", ToolName: "MultiEdit", File: abs, NewText: []string{"a", "b"}, ReplaceAll: []bool{false, true}},
		},
		{
			name: "post-write, with the tool's response",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "s1", "transcript_path": "/t.jsonl", "cwd": cwd,
				"permission_mode": "default", "tool_name": "Write", "tool_use_id": "toolu_1", "duration_ms": 12,
				"tool_input":    map[string]any{"file_path": abs, "content": "package db"},
				"tool_response": map[string]any{"filePath": abs, "type": "create"},
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Whole: true, Name: "PostToolUse", SessionID: "s1", Cwd: cwd, ToolName: "Write", File: abs, NewText: []string{"package db"}},
		},
		{
			name: "edit that deletes text",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input": map[string]any{"file_path": abs, "old_string": "x", "new_string": ""},
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", ToolName: "Edit", File: abs},
		},
		{
			name: "edit with replace_all and Claude Code's patch",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input": map[string]any{"file_path": abs, "old_string": "c", "new_string": "C\nD", "replace_all": true},
				"tool_response": map[string]any{"filePath": abs, "structuredPatch": []map[string]any{
					{"oldStart": 3, "oldLines": 5, "newStart": 3, "newLines": 6, "lines": []string{" a", " b", "-c", "+C", "+D", " e", " f"}},
					{"oldStart": 20, "oldLines": 1, "newStart": 21, "newLines": 2, "lines": []string{" g", "+h", `\ No newline at end of file`}},
				}},
			},
			want: Event{
				Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", ToolName: "Edit", File: abs, NewText: []string{"C\nD"}, ReplaceAll: []bool{true},
				Added: []Lines{{First: 5, Last: 6}, {First: 22, Last: 22}}, Patched: true,
			},
		},
		{
			name: "a patch that only removes lines",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "MultiEdit",
				"tool_input": map[string]any{"file_path": abs, "edits": []map[string]any{{"new_string": ""}}},
				"tool_response": map[string]any{"structuredPatch": []map[string]any{
					{"oldStart": 1, "oldLines": 2, "newStart": 1, "newLines": 1, "lines": []string{" a", "-b"}},
				}},
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", ToolName: "MultiEdit", File: abs, NewText: []string{""}, ReplaceAll: []bool{false}, Patched: true},
		},
		{
			name: "a patch whose counts are wrong is not used",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input": map[string]any{"file_path": abs, "new_string": "x"},
				"tool_response": map[string]any{"structuredPatch": []map[string]any{
					{"oldStart": 1, "oldLines": 1, "newStart": 1, "newLines": 5, "lines": []string{"-a", "+x"}},
				}},
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", ToolName: "Edit", File: abs, NewText: []string{"x"}, ReplaceAll: []bool{false}},
		},
		{
			name: "a response that is not an object",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input":    map[string]any{"file_path": abs, "new_string": "x"},
				"tool_response": "edited",
			},
			want: Event{Kind: adapter.PostTool, Writes: true, Name: "PostToolUse", ToolName: "Edit", File: abs, NewText: []string{"x"}, ReplaceAll: []bool{false}},
		},
		{
			name: "a tool with no file",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
			},
			want: Event{Kind: adapter.PreShell, Name: "PreToolUse", ToolName: "Bash", Command: "ls"},
		},
		{
			name: "session start: no tool, the directory Claude Code runs in",
			payload: map[string]any{
				"hook_event_name": "SessionStart", "session_id": "s1", "transcript_path": "/t.jsonl",
				"cwd": cwd, "source": "startup", "model": "claude-sonnet-5-5",
			},
			want: Event{Kind: adapter.SessionStart, Name: "SessionStart", SessionID: "s1", Source: "startup", Cwd: cwd},
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
		{
			name: "ask",
			a:    Answer{Ask: "stele.md is stele's"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"stele.md is stele's"}}`,
		},
		{
			name: "deny wins over ask",
			a:    Answer{Deny: true, Reason: "read the rules", Ask: "stele.md is stele's"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"read the rules"}}`,
		},
		{
			name: "ask with context",
			a:    Answer{Ask: "stele.md is stele's", Context: "a note"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"stele.md is stele's","additionalContext":"a note"}}`,
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

// At the start of a session, context reaches Claude before the first prompt.
func TestRenderSessionStart(t *testing.T) {
	got := string(Render("SessionStart", Answer{Context: "write with Write"}))
	want := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"write with Write"}}` + "\n"
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

// After a write, a block cannot stop the tool: it sends the reason back to
// Claude as a top-level decision.
func TestRenderPostToolUse(t *testing.T) {
	tests := []struct {
		name string
		a    Answer
		want string
	}{
		{name: "nothing to say", a: Answer{}, want: ""},
		{
			name: "block",
			a:    Answer{Deny: true, Reason: "fix line 3"},
			want: `{"decision":"block","reason":"fix line 3"}`,
		},
		{
			name: "block with context",
			a:    Answer{Deny: true, Reason: "fix line 3", Context: "also a warning"},
			want: `{"decision":"block","reason":"fix line 3","hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"also a warning"}}`,
		},
		{
			name: "context only",
			a:    Answer{Context: "a warning"},
			want: `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"a warning"}}`,
		},
		{
			// The write already happened: there is nothing left to ask.
			name: "ask is ignored",
			a:    Answer{Ask: "stele.md is stele's"},
			want: "",
		},
		{
			name: "warning to the user",
			a:    Answer{Warning: "stele could not run"},
			want: `{"systemMessage":"stele could not run"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Render("PostToolUse", tt.a))
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
