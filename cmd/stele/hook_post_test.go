package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postHook writes content to rel under root, then runs stele hook on the
// recorded PostToolUse payload in testdata/hook/<fixture>.json, pointed at
// that file.
func postHook(t *testing.T, root, fixture, rel, content string, newText ...string) (hookOutput, string) {
	t.Helper()
	return hook(t, postPayload(t, root, fixture, rel, content, newText...))
}

// postPayload writes content to rel under root and returns the payload in
// testdata/hook/<fixture>.json, pointed at that file. newText is what the
// tool wrote: the new_string of an Edit, one new_string per edit of a
// MultiEdit; none keeps the fixture's. A Write's content is the file's.
func postPayload(t *testing.T, root, fixture, rel, content string, newText ...string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdata, "hook", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	payload["cwd"] = root
	in := payload["tool_input"].(map[string]any)
	in["file_path"] = file
	switch {
	case payload["tool_name"] == "Write":
		in["content"] = content
	case len(newText) == 0:
	case payload["tool_name"] == "Edit":
		in["new_string"] = newText[0]
	case payload["tool_name"] == "MultiEdit":
		var edits []map[string]any
		for _, s := range newText {
			edits = append(edits, map[string]any{"old_string": "?", "new_string": s})
		}
		in["edits"] = edits
	}
	return payload
}

// testdata is the absolute path of this package's testdata: project moves
// the test into a temporary directory.
var testdata = func() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(wd, "testdata")
}()

func TestHookAfterWrite(t *testing.T) {
	t.Run("a write that breaks a rule", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-write", "internal/db.go", "package db\n\nvar password = 1\n")
		if o.Decision != "block" {
			t.Fatalf("decision %q, want block", o.Decision)
		}
		for _, want := range []string{"internal/db.go:3:5: rule ", "Never put credentials in code", "was saved"} {
			if !strings.Contains(o.Reason, want) {
				t.Errorf("reason lacks %q:\n%s", want, o.Reason)
			}
		}
		if o.HookSpecificOutput.PermissionDecision != "" {
			t.Error("a PostToolUse answer has a permissionDecision")
		}
	})
	t.Run("a clean write", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-write", "internal/db.go", "package db\n"); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("an edit: only its own lines count", func(t *testing.T) {
		root := project(t, checkRules)
		content := "package db\n\nvar password = 0\n\nfunc f() {\n\tpassword = 1\n}\n"
		o, _ := postHook(t, root, "post-edit", "db.go", content, "func f() {\n\tpassword = 1\n}")
		if !strings.Contains(o.Reason, "db.go:6:2:") || strings.Contains(o.Reason, "db.go:3:") {
			t.Errorf("want only line 6, the edit's, reported:\n%s", o.Reason)
		}
	})
	t.Run("a clean edit in a file that already breaks a rule", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-edit", "db.go", "var password = 0\n\nfunc f() {}\n", "func f() {}"); out != "" {
			t.Errorf("stdout %q, want nothing: older violations are left to stele check", out)
		}
	})
	t.Run("an edit with replace_all", func(t *testing.T) {
		root := project(t, checkRules)
		p := postPayload(t, root, "post-edit", "db.go", "a\npassword = 1\nb\npassword = 1\n", "password = 1")
		p["tool_input"].(map[string]any)["replace_all"] = true
		o, _ := hook(t, p)
		if !strings.Contains(o.Reason, "db.go:2:1:") || !strings.Contains(o.Reason, "db.go:4:1:") {
			t.Errorf("want both replaced lines:\n%s", o.Reason)
		}
	})
	t.Run("an edit found twice, without replace_all, is not attributed", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-edit", "db.go", "a\npassword = 1\nb\npassword = 1\n", "password = 1"); out != "" {
			t.Errorf("stdout %q, want nothing: which occurrence was written cannot be told", out)
		}
	})
	t.Run("an edit starting with a newline", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-edit", "db.go", "password = 0\npassword = 1\n", "\npassword = 1")
		if !strings.Contains(o.Reason, "db.go:2:1:") || strings.Contains(o.Reason, "db.go:1:") {
			t.Errorf("want line 2 only:\n%s", o.Reason)
		}
	})
	t.Run("an edit with Claude Code's patch: exact lines", func(t *testing.T) {
		root := project(t, checkRules)
		// The new text is also on line 7, so without the patch it would be
		// ambiguous.
		content := "package db\n\nfunc q() {\n\tpassword = 1\n}\n\nvar password = 1\n"
		o, _ := postHook(t, root, "post-edit-patch", "db.go", content)
		if !strings.Contains(o.Reason, "db.go:4:2:") || strings.Contains(o.Reason, "db.go:7:") {
			t.Errorf("want line 4 only:\n%s", o.Reason)
		}
	})
	t.Run("a multiedit with Claude Code's patch: a later edit rewrote an earlier one", func(t *testing.T) {
		root := project(t, checkRules)
		// The first edit's new text is no longer in the file.
		content := "package db\n\nfunc q() {\n\tpassword = 1\n\taudit(x)\n}\n"
		o, _ := postHook(t, root, "post-multiedit-patch", "db.go", content)
		if !strings.Contains(o.Reason, "db.go:4:2:") {
			t.Errorf("want line 4:\n%s", o.Reason)
		}
	})
	t.Run("an edit that deletes text", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-edit", "db.go", "password = 1\n", ""); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("an edit no longer in the file", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-edit", "db.go", "password = 1\n", "password = 2"); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("a multiedit", func(t *testing.T) {
		root := project(t, checkRules)
		content := "password = 0\nx := 1\npassword = 1\nvar password = 2\n"
		o, _ := postHook(t, root, "post-multiedit", "db.go", content, "x := 1", "password = 1", "var password = 2")
		if !strings.Contains(o.Reason, "db.go:3:1:") || !strings.Contains(o.Reason, "db.go:4:5:") || strings.Contains(o.Reason, "db.go:1:") {
			t.Errorf("want lines 3 and 4, not 1:\n%s", o.Reason)
		}
	})
	t.Run("an edit in a CRLF file", func(t *testing.T) {
		root := project(t, checkRules)
		content := "package db\r\n\r\nfunc f() {\r\n\tpassword = 1\r\n}\r\n"
		o, _ := postHook(t, root, "post-edit", "db.go", content, "func f() {\n\tpassword = 1\n}")
		if !strings.Contains(o.Reason, "db.go:4:2:") {
			t.Errorf("want line 4:\n%s", o.Reason)
		}
	})
	t.Run("a non-blocking rule is context, not a block", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-write", "README.md", "TODO\n")
		if o.Decision != "" {
			t.Errorf("decision %q for a non-blocking rule", o.Decision)
		}
		ctx := o.HookSpecificOutput.AdditionalContext
		if !strings.Contains(ctx, "README.md:1:1:") || !strings.Contains(ctx, "track it in an issue") || !strings.Contains(ctx, "non-blocking") {
			t.Errorf("context:\n%s", ctx)
		}
	})
	t.Run("blocking and non-blocking together", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n// TODO\n")
		if o.Decision != "block" || strings.Contains(o.Reason, "TODO") || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "a.go:2:4:") {
			t.Errorf("want a block for line 1 and context for line 2, got %+v", o)
		}
	})
	t.Run("a rule whose scope does not match", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-write", "notes.md", "password = 1\n"); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("stele.md itself", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-write", "stele.md", checkRules); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("a subproject has its own rules", func(t *testing.T) {
		root := project(t, checkRules)
		sub := "check: regex\npattern: FORBIDDEN\n\nNo forbidden words.\n"
		if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sub", "stele.md"), []byte(sub), 0o644); err != nil {
			t.Fatal(err)
		}
		o, _ := postHook(t, root, "post-write", "sub/a.go", "FORBIDDEN\npassword = 1\n")
		if !strings.Contains(o.Reason, "a.go:1:1:") || !strings.Contains(o.Reason, "No forbidden words") || strings.Contains(o.Reason, "credentials") {
			t.Errorf("want the subproject's rule only:\n%s", o.Reason)
		}
	})
	t.Run("not a write", func(t *testing.T) {
		root := project(t, checkRules)
		file := filepath.Join(root, "a.go")
		if err := os.WriteFile(file, []byte("password = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		payload := map[string]any{"hook_event_name": "PostToolUse", "session_id": "s1", "tool_name": "Read", "tool_input": map[string]any{"file_path": file}}
		if _, out := hook(t, payload); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("too many to show", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-write", "a.go", strings.Repeat("password = 1\n", 25))
		if strings.Count(o.Reason, "a.go:") != 1+20 || !strings.Contains(o.Reason, "and 5 more") { // the first line names the file too
			t.Errorf("want 20 shown and 5 more:\n%s", o.Reason)
		}
	})
	t.Run("a binary file", func(t *testing.T) {
		root := project(t, checkRules)
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\x00\n")
		if o.Decision != "" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "did not check a.go: binary") {
			t.Errorf("want a note and no block, got %+v", o)
		}
	})
	t.Run("a file too large to check", func(t *testing.T) {
		root := project(t, checkRules)
		old := maxChecked
		maxChecked = 8
		t.Cleanup(func() { maxChecked = old })
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "did not check a.go") {
			t.Errorf("want a note and no block, got %+v", o)
		}
	})
	t.Run("no session id", func(t *testing.T) {
		root := project(t, checkRules)
		file := filepath.Join(root, "a.go")
		if err := os.WriteFile(file, []byte("password = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		payload := map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Write", "tool_input": map[string]any{"file_path": file, "content": "password = 1\n"}}
		if o, _ := hook(t, payload); o.Decision != "block" {
			t.Errorf("decision %q, want block: the check needs no session", o.Decision)
		}
	})
	t.Run("backstop off: told, never blocked", func(t *testing.T) {
		root := project(t, checkRules)
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(`{"backstop": false}`), 0o644); err != nil {
			t.Fatal(err)
		}
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "a.go:1:1:") {
			t.Errorf("want the violation as context and no block, got %+v", o)
		}
	})
}

func TestHookAfterWriteOutsideStele(t *testing.T) {
	t.Run("file outside the project", func(t *testing.T) {
		project(t, checkRules)
		if _, out := postHook(t, t.TempDir(), "post-write", "x.go", "password = 1\n"); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
	})
	t.Run("no stele in the repository", func(t *testing.T) {
		root := project(t, "")
		if _, out := postHook(t, root, "post-write", "x.go", "password = 1\n"); out != "" {
			t.Errorf("stdout %q, want nothing", out)
		}
		if _, err := os.Stat(filepath.Join(root, "stele.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Error("the hook wrote stele.lock into a repository that does not use stele")
		}
	})
}

func TestHookAfterWriteFailurePolicy(t *testing.T) {
	noKey := func(t *testing.T) {
		old := keyDir
		keyDir = func() (string, error) { return "", errors.New("no config directory") }
		t.Cleanup(func() { keyDir = old })
	}
	policy := func(t *testing.T, root, json string) {
		if err := os.WriteFile(filepath.Join(root, "stele.json"), []byte(json), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("default: warn the user", func(t *testing.T) {
		root := project(t, checkRules)
		noKey(t)
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "" || !strings.Contains(o.SystemMessage, "no config directory") {
			t.Errorf("want a warning naming the failure and no block, got %+v", o)
		}
	})
	t.Run("block_on_error: block and say why", func(t *testing.T) {
		root := project(t, checkRules)
		noKey(t)
		policy(t, root, `{"block_on_error": true}`)
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "block" || !strings.Contains(o.Reason, "no config directory") {
			t.Errorf("want a block naming the failure, got %+v", o)
		}
	})
	t.Run("block_on_error with backstop off: never blocks", func(t *testing.T) {
		root := project(t, checkRules)
		noKey(t)
		policy(t, root, `{"block_on_error": true, "backstop": false}`)
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "" || !strings.Contains(o.SystemMessage, "no config directory") {
			t.Errorf("want a warning and no block, got %+v", o)
		}
	})
	t.Run("a broken stele.md keeps the last good rules", func(t *testing.T) {
		root := project(t, checkRules)
		if _, out := postHook(t, root, "post-write", "a.go", "package a\n"); out != "" {
			t.Fatalf("setup: stdout %q", out)
		}
		if err := os.WriteFile(filepath.Join(root, "stele.md"), []byte("tags: [a]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		o, _ := postHook(t, root, "post-write", "a.go", "password = 1\n")
		if o.Decision != "block" || !strings.Contains(o.HookSpecificOutput.AdditionalContext, "stele.md:1") {
			t.Errorf("want a block and the parse error as context, got %+v", o)
		}
	})
}
