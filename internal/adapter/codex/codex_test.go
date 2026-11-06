package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lictor-ai/stele/internal/adapter"
)

// recorded is a payload Codex 0.166.0 sent, from cmd/stele/testdata/hook/codex,
// its cwd moved to cwd.
func recorded(t *testing.T, name, cwd string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "stele", "testdata", "hook", "codex", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p["cwd"] = cwd
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseRecorded(t *testing.T) {
	cwd := t.TempDir()
	at := func(rel string) string { return filepath.Join(cwd, rel) }
	const turn = "01a1a883-dd1d-7e10-82d1-682ab3c8c0fe"
	tests := []struct {
		name string
		want adapter.Event
	}{
		{"session-start", adapter.Event{Kind: adapter.SessionStart, Native: start{Source: "startup"}}},
		{"user-prompt-submit", adapter.Event{Kind: adapter.Other, PromptID: turn}},
		{"stop", adapter.Event{Kind: adapter.Other, PromptID: turn}},
		{"pre-patch-add", adapter.Event{Kind: adapter.PreTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("a.txt"), Writes: true, Whole: true, NewText: []string{"one"}}}}},
		{"post-patch-add", adapter.Event{Kind: adapter.PostTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("a.txt"), Writes: true, Whole: true, NewText: []string{"one"}}}}},
		{"pre-patch-update", adapter.Event{Kind: adapter.PreTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("existing.txt"), Writes: true, NewText: []string{"hello world"}, ReplaceAll: []bool{false}}}}},
		{"post-patch-update", adapter.Event{Kind: adapter.PostTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("existing.txt"), Writes: true, NewText: []string{"hello world"}, ReplaceAll: []bool{false}}}}},
		{"pre-patch-multi", adapter.Event{Kind: adapter.PreTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("b.txt"), Writes: true, Whole: true, NewText: []string{"two"}},
			{File: at("other.txt"), Writes: true, NewText: []string{"second line"}, ReplaceAll: []bool{false}},
			{File: at("deleteme.txt"), Removes: true}}}},
		{"post-patch-multi", adapter.Event{Kind: adapter.PostTool, PromptID: turn, Edits: []adapter.Edit{
			{File: at("b.txt"), Writes: true, Whole: true, NewText: []string{"two"}},
			{File: at("other.txt"), Writes: true, NewText: []string{"second line"}, ReplaceAll: []bool{false}},
			{File: at("deleteme.txt"), Removes: true}}}},
		{"pre-bash-powershell", adapter.Event{Kind: adapter.PreShell, PromptID: turn, Command: "Set-Content -Path c.txt -Value three", Dialect: shellDialect}},
		{"post-bash", adapter.Event{Kind: adapter.Other, PromptID: turn}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(recorded(t, tt.name, cwd))
			if err != nil {
				t.Fatal(err)
			}
			tt.want.SessionID, tt.want.Cwd = "01a1a883-dc24-7932-8abd-64e03740d9f7", cwd
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
	if _, err := Parse([]byte("\xef\xbb\xbf" + `{"hook_event_name":"Stop"}`)); err != nil {
		t.Errorf("a byte order mark: %v", err)
	}
}

// patch is a PreToolUse apply_patch payload.
func patchPayload(t *testing.T, event, cwd, patch string, resp any) []byte {
	t.Helper()
	p := map[string]any{"hook_event_name": event, "session_id": "s", "turn_id": "t", "cwd": cwd,
		"tool_name": "apply_patch", "tool_input": map[string]any{"command": patch}}
	if resp != nil {
		p["tool_response"] = resp
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParsePatch(t *testing.T) {
	cwd := t.TempDir()
	at := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }
	abs := filepath.Join(t.TempDir(), "abs.go")
	tests := []struct {
		name, patch string
		want        []adapter.Edit
	}{
		{"add", "*** Begin Patch\n*** Add File: src/a.go\n+package a\n+\n+var x = 1\n*** End Patch",
			[]adapter.Edit{{File: at("src/a.go"), Writes: true, Whole: true, NewText: []string{"package a\n\nvar x = 1"}}}},
		{"update, two hunks, one only removing", "*** Begin Patch\n*** Update File: a.go\n@@ func f() {\n ctx\n-old\n+new\n+more\n@@\n-gone\n*** End of File\n*** End Patch\n",
			[]adapter.Edit{{File: at("a.go"), Writes: true, NewText: []string{"ctx\nnew\nmore"}, ReplaceAll: []bool{false}}}},
		{"delete", "*** Begin Patch\n*** Delete File: a.go\n*** End Patch",
			[]adapter.Edit{{File: at("a.go"), Removes: true}}},
		{"move", "*** Begin Patch\n*** Update File: old.go\n*** Move to: new/new.go\n@@\n-a\n+b\n*** End Patch",
			[]adapter.Edit{{File: at("old.go"), Removes: true}, {File: at("new/new.go"), Writes: true, Whole: true, NewText: []string{"b"}, ReplaceAll: []bool{false}}}},
		{"absolute path, CRLF", "*** Begin Patch\r\n*** Add File: " + abs + "\r\n+x\r\n*** End Patch\r\n",
			[]adapter.Edit{{File: abs, Writes: true, Whole: true, NewText: []string{"x"}}}},
		{"blank lines before the patch", "\n\n*** Begin Patch\n*** Delete File: a.go\n*** End Patch",
			[]adapter.Edit{{File: at("a.go"), Removes: true}}},
		{"no end: the headers still name the files", "*** Begin Patch\n*** Add File: a.go\n+x\n*** Update File: b.go\n@@\n+y",
			[]adapter.Edit{{File: at("a.go"), Writes: true}, {File: at("b.go"), Writes: true}}},
		{"a line out of place: the headers still name the files", "*** Begin Patch\n*** Add File: a.go\nnot a plus\n*** Delete File: c.go\n*** End Patch",
			[]adapter.Edit{{File: at("a.go"), Writes: true}, {File: at("c.go"), Removes: true}}},
		{"not a patch", "echo hi", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(patchPayload(t, "PreToolUse", cwd, tt.patch, nil))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got.Edits); diff != "" {
				t.Errorf("Edits (-want +got):\n%s", diff)
			}
		})
	}
}

// Patches Codex accepts leniently, and ones stele cannot follow: every file
// a header names is seen, a move's target and an indented header included.
func TestParsePatchLenient(t *testing.T) {
	cwd := t.TempDir()
	at := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }
	files := func(es []adapter.Edit) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.File)
		}
		return out
	}
	tests := []struct {
		name, patch string
		want        []string
	}{
		{"a heredoc wrapper", "<<'EOF'\n*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch\nEOF\n", []string{at("a.go")}},
		{"apply_patch and a heredoc", "apply_patch <<\"PATCH\"\n*** Begin Patch\n*** Delete File: b.go\n*** End Patch\nPATCH", []string{at("b.go")}},
		{"indented headers", "*** Begin Patch\n  *** Update File: a.go\n  @@\n-a\n+b\n  *** Delete File: stele.md\n*** End Patch", []string{at("a.go"), at("stele.md")}},
		{"a delete after an add body, indented", "*** Begin Patch\n*** Add File: a.go\n+x\n   *** Delete File: stele.md\n*** End Patch", []string{at("a.go"), at("stele.md")}},
		{"broken, with a move", "*** Begin Patch\n*** Update File: a.go\n*** Move to: .codex/hooks.json\n??\n", []string{at("a.go"), at(".codex/hooks.json")}},
		{"broken, a header inside a line", "garbage *** Delete File: stele.json\n", []string{at("stele.json")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := files(parsePatch(tt.patch, cwd))
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("files = %v, want %v", got, tt.want)
			}
		})
	}
}

// Codex runs apply_patch through its shell itself, and the hooks get it as
// Bash (measured on 0.166.0): it is read as the patch, never as a command.
func TestParseShellPatch(t *testing.T) {
	cwd := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: s.txt\n+x\n*** End Patch"
	for name, command := range map[string]any{
		"quoted, as recorded": "apply_patch '" + patch + "'",
		"a heredoc":           "apply_patch <<'EOF'\n" + patch + "\nEOF",
		"argv":                []string{"apply_patch", patch},
		"argv, applypatch":    []string{"applypatch", patch},
	} {
		t.Run(name, func(t *testing.T) {
			for _, event := range []string{"PreToolUse", "PostToolUse"} {
				data, _ := json.Marshal(map[string]any{"hook_event_name": event, "cwd": cwd, "tool_name": "Bash",
					"tool_input": map[string]any{"command": command}, "tool_response": "Invalid patch: The last line of the patch must be '*** End Patch'\n"})
				got, err := Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				want := adapter.PreTool
				if event == "PostToolUse" {
					want = adapter.PostTool
				}
				if got.Kind != want || got.Command != "" || len(got.Edits) != 1 || got.Edits[0].File != filepath.Join(cwd, "s.txt") {
					t.Errorf("%s: %+v", event, got)
				}
			}
		})
	}
	// Any other shell command after it runs is nothing to answer.
	data, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "cwd": cwd, "tool_name": "Bash", "tool_input": map[string]any{"command": "go test"}})
	if got, _ := Parse(data); got.Kind != adapter.Other {
		t.Errorf("post shell: %+v", got)
	}
}

// An argv's first word says which shell reads the script.
func TestParseShellDialect(t *testing.T) {
	for argv0, want := range map[string]adapter.Dialect{"bash": adapter.Bash, "/usr/bin/sh": adapter.Bash, `C:\Program Files\Git\bin\bash.exe`: adapter.Bash,
		"wsl": adapter.Bash, "pwsh": adapter.PowerShell, "powershell.exe": adapter.PowerShell} {
		data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": t.TempDir(), "tool_name": "Bash",
			"tool_input": map[string]any{"command": []string{argv0, "-c", "echo x > a.go"}}})
		got, _ := Parse(data)
		if got.Dialect != want || got.Command != "echo x > a.go" {
			t.Errorf("%s: dialect %v, command %q", argv0, got.Dialect, got.Command)
		}
	}
	// Any other program is its words, not its last one.
	data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": t.TempDir(), "tool_name": "Bash",
		"tool_input": map[string]any{"command": []string{"git", "commit", "-m", "x y"}}})
	if got, _ := Parse(data); !strings.Contains(got.Command, "'git' 'commit'") || got.Dialect != shellDialect {
		t.Errorf("argv: %+v", got)
	}
}

// tool_input may be the patch itself, a JSON string.
func TestParsePatchInputString(t *testing.T) {
	cwd := t.TempDir()
	data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": "apply_patch",
		"tool_input": "*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"})
	got, err := Parse(data)
	if err != nil || len(got.Edits) != 1 || got.Edits[0].File != filepath.Join(cwd, "a.go") {
		t.Errorf("got %+v, %v", got, err)
	}
}

func FuzzParsePatch(f *testing.F) {
	f.Add("*** Begin Patch\n*** Add File: a\n+x\n*** End Patch")
	f.Add("*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n-x\n+y\n*** End Patch")
	f.Fuzz(func(t *testing.T, patch string) {
		for _, e := range parsePatch(patch, "/w") {
			if e.File == "" || (!e.Writes && !e.Removes) {
				t.Fatalf("edit %+v names no file or does nothing", e)
			}
		}
	})
}

// After the patch, its result narrows the files: only those it lists, none
// when it failed.
func TestParsePatchResult(t *testing.T) {
	cwd := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: a.go\n+x\n*** Add File: b.go\n+y\n*** End Patch"
	ok := "Exit code: 0\nWall time: 0.7 seconds\nOutput:\nSuccess. Updated the following files:\nA b.go\n"
	got, _ := Parse(patchPayload(t, "PostToolUse", cwd, patch, ok))
	if len(got.Edits) != 1 || got.Edits[0].File != filepath.Join(cwd, "b.go") {
		t.Errorf("listed b.go only: %+v", got.Edits)
	}
	// A failed patch may have written some files: each is checked by its new
	// text only, never as a whole file it may not have reached.
	failed := "Exit code: 1\nWall time: 0.1 seconds\nOutput:\napply_patch verification failed\n"
	got, _ = Parse(patchPayload(t, "PostToolUse", cwd, patch, failed))
	if len(got.Edits) != 2 || got.Edits[0].Whole || got.Edits[0].NewText[0] != "x" {
		t.Errorf("after a failed patch: %+v", got.Edits)
	}
	if got, _ := Parse(patchPayload(t, "PostToolUse", cwd, patch, map[string]any{"x": 1})); len(got.Edits) != 2 {
		t.Errorf("a result not read keeps the patch's files: %+v", got.Edits)
	}
}

func TestParseShell(t *testing.T) {
	cwd := t.TempDir()
	for name, tt := range map[string]struct {
		input   map[string]any
		command string
		cwd     string
	}{
		"argv":    {map[string]any{"command": []string{"bash", "-lc", "echo x > a.go"}}, "echo x > a.go", cwd},
		"workdir": {map[string]any{"command": "echo x > a.go", "workdir": "sub"}, "echo x > a.go", filepath.Join(cwd, "sub")},
	} {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": "Bash", "tool_input": tt.input})
			got, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != adapter.PreShell || got.Command != tt.command || got.Cwd != tt.cwd {
				t.Errorf("got %+v", got)
			}
		})
	}
}

// decode reads a rendered answer as Codex's schema has it.
type output struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	SystemMessage      string `json:"systemMessage"`
	HookSpecificOutput *struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func decode(t *testing.T, data []byte) output {
	t.Helper()
	var o output
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return o
}

func TestRender(t *testing.T) {
	t.Run("pre deny, as Codex 0.166.0 took it", func(t *testing.T) {
		got := string(Render("PreToolUse", adapter.Answer{Deny: true, Reason: "rules"}))
		const want = `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"rules"}}` + "\n"
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	t.Run("an ask is a deny with the ask as the reason", func(t *testing.T) {
		o := decode(t, Render("PreToolUse", adapter.Answer{Ask: "stele.md is stele's"}))
		if o.HookSpecificOutput.PermissionDecision != "deny" || o.HookSpecificOutput.PermissionDecisionReason != "stele.md is stele's" {
			t.Errorf("got %+v", o.HookSpecificOutput)
		}
	})
	t.Run("a deny never goes without a reason, and holds the context", func(t *testing.T) {
		o := decode(t, Render("PreToolUse", adapter.Answer{Deny: true}))
		if o.HookSpecificOutput.PermissionDecisionReason == "" {
			t.Error("empty reason: Codex rejects it")
		}
		o = decode(t, Render("PreToolUse", adapter.Answer{Deny: true, Reason: "r", Context: "note"}))
		if o.HookSpecificOutput.PermissionDecisionReason != "r\n\nnote" || o.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("got %+v", o.HookSpecificOutput)
		}
	})
	t.Run("pre context alone", func(t *testing.T) {
		o := decode(t, Render("PreToolUse", adapter.Answer{Context: "note"}))
		if o.HookSpecificOutput.PermissionDecision != "" || o.HookSpecificOutput.AdditionalContext != "note" {
			t.Errorf("got %+v", o.HookSpecificOutput)
		}
	})
	t.Run("post block and context", func(t *testing.T) {
		o := decode(t, Render("PostToolUse", adapter.Answer{Deny: true, Reason: "broken", Context: "soft", Warning: "w"}))
		if o.Decision != "block" || o.Reason != "broken" || o.HookSpecificOutput.AdditionalContext != "soft" || o.SystemMessage != "w" {
			t.Errorf("got %+v", o)
		}
		if o.HookSpecificOutput.PermissionDecision != "" {
			t.Error("a post-tool answer has a permission decision")
		}
	})
	t.Run("an ask after a tool is nothing", func(t *testing.T) {
		if out := Render("PostToolUse", adapter.Answer{Ask: "x"}); out != nil {
			t.Errorf("got %s", out)
		}
	})
	t.Run("session start", func(t *testing.T) {
		o := decode(t, Render("SessionStart", adapter.Answer{Context: "use apply_patch"}))
		if o.HookSpecificOutput.HookEventName != "SessionStart" || o.HookSpecificOutput.AdditionalContext != "use apply_patch" {
			t.Errorf("got %+v", o.HookSpecificOutput)
		}
	})
	t.Run("a long context is cut to stay inline", func(t *testing.T) {
		long := strings.Repeat("a line of a long note\n", 600)
		o := decode(t, Render("PostToolUse", adapter.Answer{Context: long}))
		got := o.HookSpecificOutput.AdditionalContext
		if len(got) > maxContext || !strings.Contains(got, "stele cut the last") || !strings.HasPrefix(long, strings.SplitN(got, "\n\n(stele", 2)[0]) {
			t.Errorf("context of %d bytes: %.80q…", len(got), got)
		}
		if short := decode(t, Render("SessionStart", adapter.Answer{Context: "x"})); short.HookSpecificOutput.AdditionalContext != "x" {
			t.Error("a short context changed")
		}
	})
	t.Run("nothing to say", func(t *testing.T) {
		for _, ev := range []string{"PreToolUse", "PostToolUse", "SessionStart", "Stop", ""} {
			if out := Render(ev, adapter.Answer{}); out != nil {
				t.Errorf("%s: %s", ev, out)
			}
		}
	})
	t.Run("never allow or ask", func(t *testing.T) {
		for _, a := range []adapter.Answer{{Ask: "x"}, {Deny: true, Reason: "r"}, {Context: "c"}, {Warning: "w"}} {
			for _, ev := range []string{"PreToolUse", "PostToolUse", "SessionStart"} {
				s := string(Render(ev, a))
				if strings.Contains(s, `"allow"`) || strings.Contains(s, `"ask"`) || strings.Contains(s, "continue") {
					t.Errorf("%s %+v: %s", ev, a, s)
				}
			}
		}
	})
}
