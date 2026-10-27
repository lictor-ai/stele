// Package claudecode is the built-in Claude Code adapter: what Claude Code
// sends a hook on stdin, and what it reads back on stdout.
//
// The facts it relies on, from the Claude Code hooks documentation
// (architecture §4):
//
//   - A PreToolUse hook that answers permissionDecision "deny" stops the tool
//     call, and Claude reads permissionDecisionReason. stele denies only to
//     show rules, and otherwise leaves permissionDecision unset: "allow"
//     would skip the user's own permission prompts.
//   - permissionDecision "ask" makes Claude Code ask the user, showing
//     permissionDecisionReason in the prompt, even in auto mode. With no one
//     to answer (claude -p) the call is refused and Claude reads the reason.
//     When hooks disagree, deny wins over ask.
//   - A PostToolUse hook runs after the tool. A top-level decision "block"
//     cannot undo it: it adds reason next to the tool result, for Claude.
//     PostToolUse fires concurrently when Claude makes parallel tool calls.
//   - After an Edit or MultiEdit, tool_response carries a structuredPatch:
//     hunks of lines prefixed ' ', '-' or '+', numbered from newStart in the
//     file as written. The hooks documentation does not describe it, so a
//     patch that does not add up is ignored and stele falls back on the
//     tool's new_string.
//   - additionalContext reaches Claude alongside the tool result; systemMessage
//     reaches the user. A SessionStart or SubagentStart hook's
//     additionalContext reaches Claude at the start of the conversation, before
//     the first prompt; SessionStart fires again after /clear and compaction.
//   - Before a Bash call, tool_input carries the command line, and
//     run_in_background. stele reads the command, never runs it (#57). After
//     it, tool_response may carry a bashEditDiff, but only in auto mode or
//     with a user setting, as a beta whose shape may change: stele does not
//     use it.
//   - Exit code 0 with JSON on stdout is the only path stele uses. Exit 2
//     would block regardless of the JSON, and any other code is ignored.
package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/adapter"
)

// Event is one hook call, as stele needs it. Parse fills it from the
// payload: hook_event_name and tool_name make its Kind, Writes and Whole;
// tool_input.file_path, relative to cwd when it is not absolute, its File; a
// Bash call's command and run_in_background its Command and Background; a
// Write's content, an Edit's new_string or each MultiEdit edit's new_string
// its NewText, with their replace_all; the structuredPatch in Claude Code's
// response to an Edit or MultiEdit its Added. A SessionStart's source and a
// SubagentStart's agent_type are its Native, for Loaded.
type Event = adapter.Event

// Lines is lines First to Last of a file, 1-based and inclusive.
type Lines = adapter.Lines

// hunk is one hunk of a structuredPatch. Each line starts with ' ' for
// context, '-' for a line removed or '+' for a line added.
type hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// added returns the lines the hunks add, numbered in the new file. ok is
// false when a hunk does not add up: its counts disagree with its lines, or
// a line has no known prefix.
func added(hunks []hunk) (lines []Lines, ok bool) {
	for _, h := range hunks {
		n, olds, news := h.NewStart, 0, 0
		for _, l := range h.Lines {
			if l == "" {
				return nil, false
			}
			switch l[0] {
			case ' ':
				olds++
				news++
				n++
			case '-':
				olds++
			case '+':
				if k := len(lines); k > 0 && lines[k-1].Last == n-1 {
					lines[k-1].Last = n
				} else {
					lines = append(lines, Lines{First: n, Last: n})
				}
				news++
				n++
			case '\\': // "\ No newline at end of file"
			default:
				return nil, false
			}
		}
		if olds != h.OldLines || news != h.NewLines || (news > 0 && h.NewStart < 1) {
			return nil, false
		}
	}
	return lines, true
}

// writeTool are the tools that write a file: the post-write check reads
// their result. A Read also names a file_path. Write replaces the whole file.
var writeTool = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true}

// kind is what a hook event, run for tool, is about: Bash is the shell.
func kind(event, tool string) adapter.Kind {
	switch event {
	case "PreToolUse":
		if tool == "Bash" {
			return adapter.PreShell
		}
		return adapter.PreTool
	case "PostToolUse":
		return adapter.PostTool
	case "SessionStart":
		return adapter.SessionStart
	case "SubagentStart":
		return adapter.SubagentStart
	}
	return adapter.Other
}

// Parse reads a hook payload.
func Parse(stdin []byte) (Event, error) {
	var p struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		PromptID      string `json:"prompt_id"`
		AgentID       string `json:"agent_id"`
		AgentType     string `json:"agent_type"`
		Source        string `json:"source"`
		Cwd           string `json:"cwd"`
		ToolName      string `json:"tool_name"`
		ToolInput     struct {
			FilePath   string `json:"file_path"`
			Content    string `json:"content"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
			Edits      []struct {
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			} `json:"edits"`
			Command         string `json:"command"`
			RunInBackground bool   `json:"run_in_background"`
		} `json:"tool_input"`
		// Raw: tool_response is not an object for every tool.
		ToolResponse json.RawMessage `json:"tool_response"`
	}
	// A UTF-8 byte order mark is not JSON, but Windows tools such as
	// PowerShell add one when they pipe text; a wrapper must not disarm stele.
	stdin = bytes.TrimPrefix(stdin, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(stdin, &p); err != nil {
		return Event{}, err
	}
	if p.HookEventName == "" {
		return Event{}, errors.New("not a hook payload: no hook_event_name")
	}
	e := Event{
		Kind:      kind(p.HookEventName, p.ToolName),
		Writes:    writeTool[p.ToolName],
		Whole:     p.ToolName == "Write",
		SessionID: p.SessionID,
		PromptID:  p.PromptID,
		AgentID:   p.AgentID,
		Cwd:       p.Cwd,
	}
	if e.Kind.Starts() {
		e.Native = start{Source: p.Source, AgentType: p.AgentType}
	}
	if p.ToolName == "Bash" {
		e.Command, e.Background = p.ToolInput.Command, p.ToolInput.RunInBackground
	}
	if f := p.ToolInput.FilePath; f != "" {
		if !filepath.IsAbs(f) && p.Cwd != "" {
			f = filepath.Join(p.Cwd, f)
		}
		e.File = filepath.Clean(f)
	}
	switch {
	case p.ToolInput.Content != "":
		e.NewText = []string{p.ToolInput.Content}
	case p.ToolInput.NewString != "":
		e.NewText = []string{p.ToolInput.NewString}
		e.ReplaceAll = []bool{p.ToolInput.ReplaceAll}
	}
	for _, ed := range p.ToolInput.Edits {
		e.NewText = append(e.NewText, ed.NewString)
		e.ReplaceAll = append(e.ReplaceAll, ed.ReplaceAll)
	}
	var resp struct {
		StructuredPatch *[]hunk `json:"structuredPatch"`
	}
	if json.Unmarshal(p.ToolResponse, &resp) == nil && resp.StructuredPatch != nil {
		e.Added, e.Patched = added(*resp.StructuredPatch)
	}
	return e, nil
}

// Answer is what stele tells Claude Code.
type Answer = adapter.Answer

// Render returns the stdout for an answer to the named event, or nil when
// there is nothing to say. The exit code is always 0. Deny is
// permissionDecision "deny" before a tool and decision "block" after it;
// Ask is permissionDecision "ask", ignored after a tool; Context is
// additionalContext, for Claude; Warning is systemMessage, for the user.
func Render(event string, a Answer) []byte {
	type specific struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
		AdditionalContext        string `json:"additionalContext,omitempty"`
	}
	var out struct {
		Decision           string    `json:"decision,omitempty"`
		Reason             string    `json:"reason,omitempty"`
		HookSpecificOutput *specific `json:"hookSpecificOutput,omitempty"`
		SystemMessage      string    `json:"systemMessage,omitempty"`
	}
	post := event == "PostToolUse"
	if a.Deny && post {
		out.Decision, out.Reason = "block", a.Reason
	}
	ask := !post && !a.Deny && a.Ask != ""
	if (a.Deny && !post) || ask || a.Context != "" {
		s := &specific{HookEventName: event, AdditionalContext: a.Context}
		switch {
		case a.Deny && !post:
			s.PermissionDecision = "deny"
			s.PermissionDecisionReason = a.Reason
		case ask:
			s.PermissionDecision = "ask"
			s.PermissionDecisionReason = a.Ask
		}
		out.HookSpecificOutput = s
	}
	out.SystemMessage = a.Warning
	if out.Decision == "" && out.HookSpecificOutput == nil && out.SystemMessage == "" {
		return nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil // only strings: cannot happen
	}
	return append(data, '\n')
}
