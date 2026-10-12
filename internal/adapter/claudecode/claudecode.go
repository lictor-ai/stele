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
//   - additionalContext reaches Claude alongside the tool result; systemMessage
//     reaches the user.
//   - Exit code 0 with JSON on stdout is the only path stele uses. Exit 2
//     would block regardless of the JSON, and any other code is ignored.
package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
)

// Event is one hook call, as stele needs it.
type Event struct {
	Name      string // hook_event_name: "PreToolUse", "PostToolUse"
	SessionID string
	PromptID  string // the user turn; empty before the first prompt
	AgentID   string // empty for the main agent
	ToolName  string
	// File is the absolute path of the file the tool writes, or "" when the
	// tool writes no file stele knows how to find.
	File string
	// NewText is what the tool puts in the file: the content of a Write, the
	// new_string of an Edit, or each edit's new_string of a MultiEdit.
	NewText []string
}

// Parse reads a hook payload.
func Parse(stdin []byte) (Event, error) {
	var p struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		PromptID      string `json:"prompt_id"`
		AgentID       string `json:"agent_id"`
		Cwd           string `json:"cwd"`
		ToolName      string `json:"tool_name"`
		ToolInput     struct {
			FilePath  string `json:"file_path"`
			Content   string `json:"content"`
			NewString string `json:"new_string"`
			Edits     []struct {
				NewString string `json:"new_string"`
			} `json:"edits"`
		} `json:"tool_input"`
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
		Name:      p.HookEventName,
		SessionID: p.SessionID,
		PromptID:  p.PromptID,
		AgentID:   p.AgentID,
		ToolName:  p.ToolName,
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
	}
	for _, ed := range p.ToolInput.Edits {
		e.NewText = append(e.NewText, ed.NewString)
	}
	return e, nil
}

// Answer is what stele tells Claude Code.
type Answer struct {
	// Deny stops a PreToolUse call; Reason is what Claude reads.
	Deny   bool
	Reason string
	// Context is added to Claude's context alongside the tool result.
	Context string
	// Warning is shown to the user, not to Claude.
	Warning string
}

// Render returns the stdout for an answer to the named event, or nil when
// there is nothing to say. The exit code is always 0.
func Render(event string, a Answer) []byte {
	type specific struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
		AdditionalContext        string `json:"additionalContext,omitempty"`
	}
	var out struct {
		HookSpecificOutput *specific `json:"hookSpecificOutput,omitempty"`
		SystemMessage      string    `json:"systemMessage,omitempty"`
	}
	if a.Deny || a.Context != "" {
		s := &specific{HookEventName: event, AdditionalContext: a.Context}
		if a.Deny {
			s.PermissionDecision = "deny"
			s.PermissionDecisionReason = a.Reason
		}
		out.HookSpecificOutput = s
	}
	out.SystemMessage = a.Warning
	if out.HookSpecificOutput == nil && out.SystemMessage == "" {
		return nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil // only strings: cannot happen
	}
	return append(data, '\n')
}
