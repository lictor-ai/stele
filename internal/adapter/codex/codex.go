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
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

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
	case event == "PostToolUse" && (tool == patchTool || tool == shellTool):
		return adapter.PostTool // a shell command only when it runs apply_patch
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
	post := p.HookEventName == "PostToolUse"
	switch {
	case e.Kind == adapter.SessionStart:
		e.Native = start{Source: p.Source}
	case p.ToolName == shellTool && (e.Kind == adapter.PreShell || e.Kind == adapter.PostTool):
		var in struct {
			Command json.RawMessage `json:"command"`
			Workdir string          `json:"workdir"`
		}
		if json.Unmarshal(p.ToolInput, &in) != nil {
			e.Kind = adapter.Other
			return e, nil
		}
		if in.Workdir != "" {
			if w, ok := patchPath(in.Workdir, p.Cwd); ok {
				e.Cwd = w
			}
		}
		command, argvPatch := shellCommand(in.Command)
		// Codex applies a patch run through its shell itself, measured on
		// 0.166.0: apply_patch '<patch>' reaches the hooks as Bash. It is
		// read as the patch it is.
		patch, isPatch := argvPatch, argvPatch != ""
		if !isPatch {
			patch, isPatch = shellPatch(command)
		}
		switch {
		case isPatch:
			e.Kind = adapter.PreTool
			if post {
				e.Kind = adapter.PostTool
			}
			e.Edits = parsePatch(patch, e.Cwd)
			if post {
				e.Edits = applied(e.Edits, p.ToolResponse, e.Cwd)
			}
		case post:
			e.Kind = adapter.Other // nothing runs after a shell command
		default:
			e.Command, e.Dialect = command, shellDialect
		}
	case e.Kind == adapter.PreTool || e.Kind == adapter.PostTool:
		e.Edits = parsePatch(patchInput(p.ToolInput), p.Cwd)
		if post {
			e.Edits = applied(e.Edits, p.ToolResponse, p.Cwd)
		}
	}
	return e, nil
}

// patchInput is an apply_patch's patch: tool_input's command or input, or
// tool_input itself when it is a string.
func patchInput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var in struct {
		Command string `json:"command"`
		Input   string `json:"input"`
	}
	json.Unmarshal(raw, &in)
	if in.Command != "" {
		return in.Command
	}
	return in.Input
}

// patchCommand matches a shell command that runs apply_patch.
var patchCommand = regexp.MustCompile(`^\s*(?:apply_patch|applypatch)(?:\s|$)`)

// shellCommand is a Bash call's command: a string, or an argv whose last
// word is the script a shell runs, such as ["bash", "-lc", "…"]. An argv
// that runs apply_patch itself gives its patch instead, never as a command.
func shellCommand(raw json.RawMessage) (command, patch string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, ""
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return "", ""
	}
	name := strings.ToLower(filepath.Base(strings.ReplaceAll(argv[0], `\`, "/")))
	name = strings.TrimSuffix(name, ".exe")
	if name == "apply_patch" || name == "applypatch" {
		if len(argv) > 1 {
			return "", argv[1]
		}
		return "", " "
	}
	return argv[len(argv)-1], ""
}

// mentionsPatch matches a command that names apply_patch anywhere: after cd,
// in a pipe, in a script.
var mentionsPatch = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:apply_patch|applypatch)(?:[^A-Za-z0-9_]|$)`)

// shellPatch reads the patch of a shell command that runs apply_patch: a
// heredoc, apply_patch <<'EOF' …, a PowerShell here-string, apply_patch
// @'…'@, or a quoted argument. ok is false for a command that never names
// apply_patch. One whose patch stele cannot cut out, such as cd x &&
// apply_patch … or … | apply_patch, gives the whole command, so every header
// in it names a file for the guard.
func shellPatch(command string) (patch string, ok bool) {
	if !patchCommand.MatchString(command) {
		if mentionsPatch.MatchString(command) {
			return command, true
		}
		return "", false
	}
	rest := strings.TrimLeft(command, " \t")
	rest = strings.TrimLeft(rest[strings.IndexAny(rest+" ", " \t\n"):], " \t")
	switch {
	case strings.HasPrefix(rest, "<<"):
		return unwrap(command), true
	case strings.HasPrefix(rest, "@'"), strings.HasPrefix(rest, `@"`):
		end := "\n" + rest[1:2] + "@"
		body := strings.TrimPrefix(strings.TrimPrefix(rest[2:], "\r"), "\n")
		if i := strings.Index(body, end); i >= 0 {
			return strings.TrimSuffix(body[:i], "\r"), true
		}
		return command, true
	case strings.HasPrefix(rest, "'"):
		var b strings.Builder
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' { // PowerShell's '' is a quote
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), true
			}
			b.WriteByte(rest[i])
		}
	case strings.HasPrefix(rest, `"`):
		if s, err := strconv.Unquote(firstQuoted(rest)); err == nil {
			return s, true
		}
	}
	return command, true
}

// firstQuoted is the double-quoted string at the start of s.
func firstQuoted(s string) string {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return s[:i+1]
		}
	}
	return s
}

// applied narrows a patch's edits to what its result says it did: the files
// it lists. A patch that failed may still have written some of its files:
// each is checked, by its new text only, so a file the patch never reached is
// not judged on what it held. A result stele cannot read keeps them all.
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
		out := make([]adapter.Edit, 0, len(edits))
		for _, e := range edits {
			if e.Whole {
				e.Whole = false
				e.ReplaceAll = make([]bool, len(e.NewText))
			}
			out = append(out, e)
		}
		return out
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
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: fitContext(a.Context)}
		}
	case "PostToolUse":
		if a.Deny {
			out.Decision, out.Reason = "block", a.Reason
			if out.Reason == "" {
				out.Reason = refused
			}
		}
		if a.Context != "" {
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: fitContext(a.Context)}
		}
	case "SessionStart":
		if a.Context != "" {
			out.HookSpecificOutput = &specific{HookEventName: event, AdditionalContext: fitContext(a.Context)}
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

// maxContext caps additionalContext: Codex moves a hook's context longer
// than its additionalContextLimit, 2,500 tokens by default, into a file the
// model must open, so stele keeps it well under, at about 1,500 tokens.
const maxContext = 6000

// fitContext cuts s to maxContext bytes, at a line end, and says so.
func fitContext(s string) string {
	if len(s) <= maxContext {
		return s
	}
	cut := s[:maxContext-200]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\n\n(stele cut the last %d bytes of this note to keep it in your context: run stele check for everything.)", len(s)-len(cut))
}

// join joins two texts with a blank line, either may be empty.
func join(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}
