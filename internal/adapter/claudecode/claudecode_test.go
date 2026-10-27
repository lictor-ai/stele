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
		want    adapter.Event
	}{
		{
			name: "write",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": "p1",
				"tool_name": "Write", "tool_input": map[string]any{"file_path": abs, "content": "package db"},
			},
			want: adapter.Event{Kind: adapter.PreTool, Writes: true, Whole: true, SessionID: "s1", PromptID: "p1", File: abs, NewText: []string{"package db"}},
		},
		{
			name: "edit from a subagent, path relative to cwd",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "s1", "agent_id": "a1", "cwd": cwd,
				"tool_name": "Edit", "tool_input": map[string]any{"file_path": "internal/db.go", "old_string": "x", "new_string": "y"},
			},
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, SessionID: "s1", AgentID: "a1", Cwd: cwd, File: abs, NewText: []string{"y"}, ReplaceAll: []bool{false}},
		},
		{
			name: "bash",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "session_id": "s1", "cwd": cwd, "tool_name": "Bash", "tool_use_id": "toolu_2",
				"tool_input": map[string]any{"command": "cat > a.go <<EOF\nx\nEOF", "description": "write a.go", "run_in_background": true},
			},
			want: adapter.Event{Kind: adapter.PreShell, SessionID: "s1", Cwd: cwd, Command: "cat > a.go <<EOF\nx\nEOF", Background: true},
		},
		{
			name: "multiedit",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "MultiEdit",
				"tool_input": map[string]any{"file_path": abs, "edits": []map[string]any{{"new_string": "a"}, {"new_string": "b", "replace_all": true}}},
			},
			want: adapter.Event{Kind: adapter.PreTool, Writes: true, File: abs, NewText: []string{"a", "b"}, ReplaceAll: []bool{false, true}},
		},
		{
			name: "post-write, with the tool's response",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "s1", "transcript_path": "/t.jsonl", "cwd": cwd,
				"permission_mode": "default", "tool_name": "Write", "tool_use_id": "toolu_1", "duration_ms": 12,
				"tool_input":    map[string]any{"file_path": abs, "content": "package db"},
				"tool_response": map[string]any{"filePath": abs, "type": "create"},
			},
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, Whole: true, SessionID: "s1", Cwd: cwd, File: abs, NewText: []string{"package db"}},
		},
		{
			name: "edit that deletes text",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input": map[string]any{"file_path": abs, "old_string": "x", "new_string": ""},
			},
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, File: abs},
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
			want: adapter.Event{
				Kind: adapter.PostTool, Writes: true, File: abs, NewText: []string{"C\nD"}, ReplaceAll: []bool{true},
				Added: []adapter.Lines{{First: 5, Last: 6}, {First: 22, Last: 22}}, Patched: true,
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
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, File: abs, NewText: []string{""}, ReplaceAll: []bool{false}, Patched: true},
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
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, File: abs, NewText: []string{"x"}, ReplaceAll: []bool{false}},
		},
		{
			name: "a response that is not an object",
			payload: map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Edit",
				"tool_input":    map[string]any{"file_path": abs, "new_string": "x"},
				"tool_response": "edited",
			},
			want: adapter.Event{Kind: adapter.PostTool, Writes: true, File: abs, NewText: []string{"x"}, ReplaceAll: []bool{false}},
		},
		{
			name: "a tool with no file",
			payload: map[string]any{
				"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
			},
			want: adapter.Event{Kind: adapter.PreShell, Command: "ls"},
		},
		{
			name: "session start: no tool, the directory Claude Code runs in",
			payload: map[string]any{
				"hook_event_name": "SessionStart", "session_id": "s1", "transcript_path": "/t.jsonl",
				"cwd": cwd, "source": "startup", "model": "claude-sonnet-5-5",
			},
			want: adapter.Event{Kind: adapter.SessionStart, SessionID: "s1", Cwd: cwd, Native: start{Source: "startup"}},
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
		a    adapter.Answer
		want string // "" means no output
	}{
		{name: "nothing to say", a: adapter.Answer{}, want: ""},
		{
			name: "deny",
			a:    adapter.Answer{Deny: true, Reason: "read the rules"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"read the rules"}}`,
		},
		{
			// Allowing never says "allow": that would skip the user's prompts.
			name: "allow with context",
			a:    adapter.Answer{Context: "stele.md has an error"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"stele.md has an error"}}`,
		},
		{
			name: "warning to the user",
			a:    adapter.Answer{Warning: "stele could not run"},
			want: `{"systemMessage":"stele could not run"}`,
		},
		{
			name: "ask",
			a:    adapter.Answer{Ask: "stele.md is stele's"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"stele.md is stele's"}}`,
		},
		{
			name: "deny wins over ask",
			a:    adapter.Answer{Deny: true, Reason: "read the rules", Ask: "stele.md is stele's"},
			want: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"read the rules"}}`,
		},
		{
			name: "ask with context",
			a:    adapter.Answer{Ask: "stele.md is stele's", Context: "a note"},
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
	got := string(Render("SessionStart", adapter.Answer{Context: "write with Write"}))
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
		a    adapter.Answer
		want string
	}{
		{name: "nothing to say", a: adapter.Answer{}, want: ""},
		{
			name: "block",
			a:    adapter.Answer{Deny: true, Reason: "fix line 3"},
			want: `{"decision":"block","reason":"fix line 3"}`,
		},
		{
			name: "block with context",
			a:    adapter.Answer{Deny: true, Reason: "fix line 3", Context: "also a warning"},
			want: `{"decision":"block","reason":"fix line 3","hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"also a warning"}}`,
		},
		{
			name: "context only",
			a:    adapter.Answer{Context: "a warning"},
			want: `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"a warning"}}`,
		},
		{
			// The write already happened: there is nothing left to ask.
			name: "ask is ignored",
			a:    adapter.Answer{Ask: "stele.md is stele's"},
			want: "",
		},
		{
			name: "warning to the user",
			a:    adapter.Answer{Warning: "stele could not run"},
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
	if err != nil || got.Kind != adapter.PreTool {
		t.Errorf("Parse with a BOM = %+v, %v", got, err)
	}
}
