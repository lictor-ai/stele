// Package codex is the built-in adapter for OpenAI's Codex CLI: what Codex
// sends a hook on stdin, and what it reads back on stdout (ADR-0012).
//
// The facts it relies on, measured on Codex 0.166.0 on Windows on 2026-11-04
// (the payloads are under cmd/stele/testdata/hook/codex):
//
//   - Hooks are configured in .codex/hooks.json, in Claude Code's shape. A
//     project's hooks run only in a project the user trusts, and only once the
//     user approved each one with /hooks; the approval is keyed by the hook's
//     place in the file and a hash of its settings, so a changed command
//     needs a new one. codex exec skips a hook not approved, silently.
//   - Codex writes files with apply_patch, whose tool_input.command is one
//     patch in Codex's own format: several files, each added, updated (and
//     maybe moved) or deleted, paths relative to cwd. After it, tool_response
//     is a string ending in the files changed, "A b.txt", "M x", "D y".
//   - The shell tool is named Bash on every OS; on Windows its command is
//     PowerShell. Every payload of a turn carries the same turn_id.
//   - A PreToolUse answer of permissionDecision "deny", with a reason that is
//     not empty, stops the call, and the model reads "Command blocked by
//     PreToolUse hook: <reason>". "allow" and "ask" are refused: a hook
//     cannot ask the user.
//   - After a tool, decision "block" with a reason replaces the result the
//     model reads with the reason; the file is already written.
//     hookSpecificOutput.additionalContext reaches the model as a developer
//     message, after a tool and at session start; systemMessage the user.
//   - SessionStart's source is startup, resume, clear, compact or fork.
//   - Hook commands run through powershell.exe -Command on Windows, which
//     turns an exit code 2 into 1: only JSON on stdout with exit 0 is used.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"

	"github.com/lictor-ai/stele/internal/adapter"
)

// The tool names Codex sends.
const (
	patchTool = "apply_patch"
	shellTool = "Bash"
)

// kind is what a hook event, run for tool, is about.
func kind(event, tool string) adapter.Kind {
	switch {
	case event == "PreToolUse" && tool == patchTool:
		return adapter.PreTool
	case event == "PreToolUse" && tool == shellTool:
		return adapter.PreShell
	case event == "PostToolUse" && tool == patchTool:
		return adapter.PostTool
	case event == "SessionStart":
		return adapter.SessionStart
	}
	return adapter.Other
}

// start is a SessionStart's own field, which Parse keeps in Event.Native for
// Loaded.
type start struct {
	Source string // "startup", "resume", "clear", "compact", "fork"
}

// shellDialect is the language Codex's shell reads on this OS.
var shellDialect = func() adapter.Dialect {
	if runtime.GOOS == "windows" {
		return adapter.PowerShell
	}
	return adapter.Bash
}()

// Parse reads a hook payload into an Event: hook_event_name and tool_name
// make its Kind; turn_id is the prompt; an apply_patch's patch its Edits,
// narrowed after the call to the files the result says it changed, none
// when it failed; a Bash call's command its Command.
func Parse(stdin []byte) (adapter.Event, error) {
	var p struct {
		HookEventName string          `json:"hook_event_name"`
		SessionID     string          `json:"session_id"`
		TurnID        string          `json:"turn_id"`
		AgentID       string          `json:"agent_id"`
		Source        string          `json:"source"`
		Cwd           string          `json:"cwd"`
		ToolName      string          `json:"tool_name"`
		ToolInput     json.RawMessage `json:"tool_input"`
		ToolResponse  json.RawMessage `json:"tool_response"`
	}
	// A wrapper on Windows may add a byte order mark; it is not JSON.
	stdin = bytes.TrimPrefix(stdin, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(stdin, &p); err != nil {
		return adapter.Event{}, err
	}
	if p.HookEventName == "" {
		return adapter.Event{}, errors.New("not a hook payload: no hook_event_name")
	}
	e := adapter.Event{
		Kind:      kind(p.HookEventName, p.ToolName),
		SessionID: p.SessionID,
		PromptID:  p.TurnID,
		AgentID:   p.AgentID,
		Cwd:       p.Cwd,
	}
	switch e.Kind {
	case adapter.SessionStart:
		e.Native = start{Source: p.Source}
	case adapter.PreShell:
		var in struct {
			Command json.RawMessage `json:"command"`
			Workdir string          `json:"workdir"`
		}
		if json.Unmarshal(p.ToolInput, &in) == nil {
			e.Command = shellCommand(in.Command)
			if in.Workdir != "" {
				if w, ok := patchPath(in.Workdir, p.Cwd); ok {
					e.Cwd = w
				}
			}
		}
		e.Dialect = shellDialect
	case adapter.PreTool, adapter.PostTool:
		var in struct {
			Command string `json:"command"`
			Input   string `json:"input"`
		}
		if json.Unmarshal(p.ToolInput, &in) != nil {
			return e, nil
		}
		patch := in.Command
		if patch == "" {
			patch = in.Input
		}
		e.Edits = parsePatch(patch, p.Cwd)
		if e.Kind == adapter.PostTool {
			e.Edits = applied(e.Edits, p.ToolResponse, p.Cwd)
		}
	}
	return e, nil
}

// shellCommand is a Bash call's command: a string, or an argv whose last
// word is the script a shell runs, such as ["bash", "-lc", "…"].
func shellCommand(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
		return argv[len(argv)-1]
	}
	return ""
}

// applied narrows a patch's edits to what its result says it did: nothing
// when it failed, else the files it lists. A result stele cannot read keeps
// them all.
func applied(edits []adapter.Edit, raw json.RawMessage, cwd string) []adapter.Edit {
	var resp string
	if json.Unmarshal(raw, &resp) != nil {
		return edits
	}
	changed, failed, ok := patchResult(resp)
	switch {
	case !ok:
		return edits
	case failed:
		return nil
	}
	listed := map[string]bool{}
	for p := range changed {
		if f, ok := patchPath(p, cwd); ok {
			listed[f] = true
		}
	}
	var out []adapter.Edit
	for _, e := range edits {
		if listed[e.File] {
			out = append(out, e)
		}
	}
	return out
}

// eventNames are the hook events each kind answers.
var eventNames = map[adapter.Kind]string{
	adapter.PreTool:      "PreToolUse",
	adapter.PreShell:     "PreToolUse",
	adapter.PostTool:     "PostToolUse",
	adapter.SessionStart: "SessionStart",
}

// refused is the reason of a deny that would have none: Codex rejects a deny
// without one.
const refused = "stele stopped this call."

// Render returns the stdout for an answer to the named event, or nil when
// there is nothing to say. The exit code is always 0. Before a tool, Deny is
// permissionDecision "deny", and so is an Ask, which Codex cannot put to the
// user; a deny's reason holds the context too. After a tool, Deny is
// decision "block". Context is additionalContext, for the model; Warning is
// systemMessage, for the user. Codex never reads "allow" or "ask" from stele.
func Render(event string, a adapter.Answer) []byte {
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
	switch event {
	case "PreToolUse":
		if a.Deny || a.Ask != "" {
			reason := a.Reason
			if !a.Deny {
				reason = a.Ask
			}
			reason = join(reason, a.Context)
			if reason == "" {
				reason = refused
			}
			out.HookSpecificOutput = &specific{HookEventName: event, PermissionDecision: "deny", PermissionDecisionReason: reason}
		} else if a.Context != "" {
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: a.Context}
		}
	case "PostToolUse":
		if a.Deny {
			out.Decision, out.Reason = "block", a.Reason
			if out.Reason == "" {
				out.Reason = refused
			}
		}
		if a.Context != "" {
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: a.Context}
		}
	case "SessionStart":
		if a.Context != "" {
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: a.Context}
		}
	default:
		return nil
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

// join joins two texts with a blank line, either may be empty.
func join(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}
