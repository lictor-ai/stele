package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// preWrite is a PreToolUse payload for tool (Write, Edit or MultiEdit) on file.
func preWrite(tool, file string) map[string]any {
	in := map[string]any{"file_path": file}
	switch tool {
	case "Write":
		in["content"] = "x"
	case "Edit":
		in["old_string"], in["new_string"] = "a", "b"
	case "MultiEdit":
		in["edits"] = []map[string]any{{"old_string": "a", "new_string": "b"}}
	}
	return map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": "p1",
		"tool_name": tool, "tool_input": in,
	}
}

// asks reports whether the hook asks the user before the write, naming rel.
func asks(t *testing.T, o hookOutput, rel string) bool {
	t.Helper()
	h := o.HookSpecificOutput
	if h.PermissionDecision != "ask" {
		return false
	}
	if !strings.Contains(h.PermissionDecisionReason, rel) {
		t.Errorf("ask reason %q does not name %s", h.PermissionDecisionReason, rel)
	}
	if strings.Contains(h.PermissionDecisionReason, "Never build SQL") {
		t.Errorf("ask reason carries rule text:\n%s", h.PermissionDecisionReason)
	}
	return true
}

// A write that changes one of stele's own files can weaken stele or turn it
// off, so the user is asked every time (#52).
func TestHookAsksForOwnFiles(t *testing.T) {
	clock(t, time.Now())
	own := []string{
		"stele.md", "stele.json", "stele.lock",
		".stele/index.json", ".stele/state/s1.json",
		// A new subproject with no rules is another way to turn stele off.
		"sub/stele.md", "sub/stele.json", "sub/.stele/x",
		// Windows writes these to the real file.
		"stele.json.", "stele.md::$DATA",
	}
	for _, tool := range []string{"Write", "Edit", "MultiEdit"} {
		for _, rel := range own {
			t.Run(tool+" "+rel, func(t *testing.T) {
				root := project(t, hookRules)
				o, out := hook(t, preWrite(tool, filepath.Join(root, filepath.FromSlash(rel))))
				if !asks(t, o, strings.TrimRight(rel, ".")) {
					t.Errorf("a %s to %s: %s, want ask", tool, rel, out)
				}
			})
		}
	}
	for _, rel := range []string{"notstele.md", "docs/stele.md.bak"} {
		t.Run("not "+rel, func(t *testing.T) {
			root := project(t, hookRules)
			if o, _ := hook(t, preWrite("Write", filepath.Join(root, rel))); o.HookSpecificOutput.PermissionDecision == "ask" {
				t.Errorf("asked for %s", rel)
			}
		})
	}
	t.Run("case", func(t *testing.T) {
		root := project(t, hookRules)
		o, _ := hook(t, preWrite("Write", filepath.Join(root, "STELE.MD")))
		folds := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
		if got := o.HookSpecificOutput.PermissionDecision == "ask"; got != folds {
			t.Errorf("STELE.MD asked = %v, want %v on %s", got, folds, runtime.GOOS)
		}
	})
}

// Asking records nothing: the next write in the turn still shows its rules.
func TestHookAskKeepsTheTurn(t *testing.T) {
	clock(t, time.Now())
	root := project(t, hookRules)
	if o, _ := hook(t, preWrite("Write", filepath.Join(root, "stele.md"))); o.HookSpecificOutput.PermissionDecision != "ask" {
		t.Fatal("no ask for stele.md")
	}
	o, _ := hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "Never build SQL") {
		t.Errorf("the write to a.go after the ask: %+v, want a deny with its rules", o.HookSpecificOutput)
	}
}

// Asking does not depend on stele.json: it is what the agent would change.
func TestHookAsksWhateverTheConfig(t *testing.T) {
	clock(t, time.Now())
	for name, setup := range map[string]func(t *testing.T, root string){
		"backstop off": func(t *testing.T, root string) {
			writeAt(t, root, "stele.json", `{"backstop": false}`)
		},
		"stele.json invalid": func(t *testing.T, root string) {
			writeAt(t, root, "stele.json", `{"backstop": `)
		},
		"block_on_error and no key": func(t *testing.T, root string) {
			writeAt(t, root, "stele.json", `{"block_on_error": true}`)
			old := keyDir
			keyDir = func() (string, error) { return "", errors.New("no config directory") }
			t.Cleanup(func() { keyDir = old })
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := project(t, hookRules)
			setup(t, root)
			o, out := hook(t, preWrite("Edit", filepath.Join(root, "stele.json")))
			if !asks(t, o, "stele.json") {
				t.Errorf("%s: %s, want ask", name, out)
			}
		})
	}
	t.Run("no session id", func(t *testing.T) {
		root := project(t, hookRules)
		p := preWrite("Write", filepath.Join(root, "stele.md"))
		delete(p, "session_id")
		if o, out := hook(t, p); !asks(t, o, "stele.md") {
			t.Errorf("%s, want ask", out)
		}
	})
}

// A link that leads to one of stele's own files is asked for too.
func TestHookAsksThroughLinks(t *testing.T) {
	clock(t, time.Now())
	t.Run("symlink to stele.md", func(t *testing.T) {
		root := project(t, hookRules)
		link := filepath.Join(root, "rules.md")
		if err := os.Symlink(filepath.Join(root, "stele.md"), link); err != nil {
			t.Skipf("cannot make a symlink: %v", err)
		}
		if o, out := hook(t, preWrite("Write", link)); o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("write through a symlink to stele.md: %s, want ask", out)
		}
	})
	t.Run("directory link to .stele", func(t *testing.T) {
		root := project(t, hookRules)
		if err := os.MkdirAll(filepath.Join(root, ".stele"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "cache")
		if err := os.Symlink(filepath.Join(root, ".stele"), link); err != nil {
			if runtime.GOOS != "windows" {
				t.Skipf("cannot make a symlink: %v", err)
			}
			// A junction needs no privilege.
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, filepath.Join(root, ".stele")).CombinedOutput(); err != nil {
				t.Skipf("cannot make a symlink or a junction: %v\n%s", err, out)
			}
		}
		if o, out := hook(t, preWrite("Write", filepath.Join(link, "index.json"))); o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("write through a link to .stele/: %s, want ask", out)
		}
	})
	t.Run("hard link to stele.md", func(t *testing.T) {
		root := project(t, hookRules)
		link := filepath.Join(root, "rules.md")
		if err := os.Link(filepath.Join(root, "stele.md"), link); err != nil {
			t.Skipf("cannot make a hard link: %v", err)
		}
		if o, out := hook(t, preWrite("Write", link)); o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("write to a hard link of stele.md: %s, want ask", out)
		}
	})
}

// link makes link point to target: a symlink, or on Windows without the
// privilege a junction, when target is a directory.
func link(t *testing.T, target, link string, dir bool) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" || !dir {
		t.Skipf("cannot make a symlink: %v", err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot make a symlink or a junction: %v\n%s", err, out)
	}
}

// A path in no stele project can still lead into one.
func TestHookAsksForALinkFromOutside(t *testing.T) {
	clock(t, time.Now())
	t.Run("to stele.md", func(t *testing.T) {
		root := project(t, hookRules)
		l := filepath.Join(t.TempDir(), "rules.md")
		link(t, filepath.Join(root, "stele.md"), l, false)
		if o, out := hook(t, preWrite("Write", l)); o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("write through a link from outside to stele.md: %s, want ask", out)
		}
	})
	t.Run("to .stele", func(t *testing.T) {
		root := project(t, hookRules)
		if err := os.MkdirAll(filepath.Join(root, ".stele"), 0o755); err != nil {
			t.Fatal(err)
		}
		l := filepath.Join(t.TempDir(), "cache")
		link(t, filepath.Join(root, ".stele"), l, true)
		if o, out := hook(t, preWrite("Write", filepath.Join(l, "index.json"))); o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("write through a link from outside to .stele/: %s, want ask", out)
		}
	})
	t.Run("to a plain file", func(t *testing.T) {
		root := project(t, hookRules)
		writeAt(t, root, "a.go", "package a\n")
		l := filepath.Join(t.TempDir(), "a.go")
		link(t, filepath.Join(root, "a.go"), l, false)
		if o, out := hook(t, preWrite("Write", l)); o.HookSpecificOutput.PermissionDecision == "ask" {
			t.Errorf("asked for a link from outside to a.go: %s", out)
		}
	})
}

func TestHookDoesNotAsk(t *testing.T) {
	clock(t, time.Now())
	t.Run("a repository that does not use stele", func(t *testing.T) {
		root := project(t, "")
		_, out := hook(t, preWrite("Write", filepath.Join(root, "stele.json")))
		if out != "" {
			t.Errorf("stdout %q: writing stele.json opts a repository in, nobody to ask for", out)
		}
		if _, err := os.Stat(filepath.Join(root, ".stele")); err == nil {
			t.Error("the hook created .stele/ in a repository that does not use stele")
		}
	})
	t.Run("a read", func(t *testing.T) {
		root := project(t, hookRules)
		p := map[string]any{
			"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": "p1",
			"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(root, "stele.md")},
		}
		if o, _ := hook(t, p); o.HookSpecificOutput.PermissionDecision == "ask" {
			t.Error("asked before a Read of stele.md")
		}
	})
}

// Once the user allows it, the write goes through and the hook recompiles:
// the next write shows the new rule.
func TestHookAfterAnAllowedRulesWrite(t *testing.T) {
	clock(t, time.Now())
	root := project(t, hookRules)
	newRules := hookRules + "\n---\n\nKeep functions short.\n"
	o, out := postHook(t, root, "post-write", "stele.md", newRules)
	if o.Decision == "block" {
		t.Fatalf("a write to stele.md was blocked after it: %s", out)
	}
	o, _ = hook(t, write(filepath.Join(root, "a.go"), "p1", ""))
	if !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "Keep functions short.") {
		t.Errorf("the next write does not show the new rule:\n%s", o.HookSpecificOutput.PermissionDecisionReason)
	}
}
