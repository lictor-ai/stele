package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/sign"
)

// signingKeyFile makes this test's key, in the temp directory project
// points keyDir at, and returns its directory and file.
func signingKeyFile(t *testing.T) (dir, key string) {
	t.Helper()
	dir, err := keyDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(); err != nil {
		t.Fatal(err)
	}
	return dir, sign.KeyFile(dir)
}

// With the signing key, the agent could sign log lines stele never wrote;
// with a new one, stele ignores every line signed before. A write to it asks,
// as one to stele's own files does (#152).
func TestHookAsksForTheSigningKey(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	dir, key := signingKeyFile(t)
	files := []string{
		key,
		filepath.Join(dir, "KEY"), // another case
		key + ".",                 // Windows writes it to the key
		key + "::$DATA",           // a stream of the key
		filepath.Join(dir, "x", "..", "key"),
	}
	for _, tool := range []string{"Write", "Edit", "MultiEdit"} {
		for _, f := range files {
			o, out := hook(t, inSession(tool, f, root))
			if !asks(t, o, filepath.ToSlash(filepath.Clean(f))) {
				t.Errorf("a %s to %s: %s, want ask", tool, f, out)
			}
		}
	}
	// A hard link to the key is the key.
	hard := filepath.Join(t.TempDir(), "k")
	if err := os.Link(key, hard); err != nil {
		t.Logf("no hard link: %v", err)
	} else if o, out := hook(t, inSession("Write", hard, root)); o.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("a hard link to the key: %s, want ask", out)
	}
	// Reading the key, and writing beside it or elsewhere in the config
	// directory, are not stele's business.
	read := map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s1", "prompt_id": "p1", "cwd": root,
		"tool_name": "Read", "tool_input": map[string]any{"file_path": key},
	}
	if o, out := hook(t, read); o.HookSpecificOutput.PermissionDecision != "" {
		t.Errorf("a Read of the key: %s", out)
	}
	for _, f := range []string{
		filepath.Join(dir, "notes.txt"),
		filepath.Join(dir, "keys"),
		filepath.Join(filepath.Dir(dir), "other", "key"),
	} {
		if o, out := hook(t, inSession("Write", f, root)); o.HookSpecificOutput.PermissionDecision != "" {
			t.Errorf("a Write to %s: %s", f, out)
		}
	}
}

// A shell command that writes, removes or moves the key, or removes the
// folder it is in, asks too; one that reads it does not.
func TestHookBashAsksForTheSigningKey(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	dir, _ := signingKeyFile(t)
	t.Setenv("KEYDIR", dir)
	for _, command := range []string{
		"echo x > $KEYDIR/key",
		"printf x >> $KEYDIR/key",
		"rm $KEYDIR/key",
		"mv $KEYDIR/key /dev/null",
		"cp x $KEYDIR/key",
		"rm -rf $KEYDIR",
		"mv $KEYDIR old",
		"rm -rf $KEYDIR/..",
	} {
		o, out := hook(t, bash(command, root, "p1", ""))
		if o.HookSpecificOutput.PermissionDecision != "ask" {
			t.Errorf("%q: %s, want ask", command, out)
		}
	}
	for _, command := range []string{
		"cat $KEYDIR/key",
		"cp $KEYDIR/key copy",
		"echo x > $KEYDIR/notes.txt",
		"rm $KEYDIR/../other",
	} {
		if o, out := hook(t, bash(command, root, "p1", "")); o.HookSpecificOutput.PermissionDecision != "" {
			t.Errorf("%q: %s, want no decision", command, out)
		}
	}
}

// PowerShell, Codex's shell on Windows, is read for the key too.
func TestBeforeBashPowerShellSigningKey(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	_, key := signingKeyFile(t)
	ev := adapter.Event{Kind: adapter.PreShell, SessionID: "s1", PromptID: "p1", Cwd: root, Dialect: adapter.PowerShell}
	for _, command := range []string{
		"Remove-Item '" + key + "'",
		"Set-Content -Path '" + key + "' -Value x",
		"'x' > '" + key + "'",
	} {
		ev.Command = command
		if a := beforeBash(ev, adapter.Switches{}); a.Ask == "" || !strings.Contains(a.Ask, filepath.ToSlash(key)) {
			t.Errorf("%q: %+v, want an ask naming the key", command, a)
		}
	}
	ev.Command = "Get-Content '" + key + "'"
	if a := beforeBash(ev, adapter.Switches{}); a.Ask != "" || a.Deny {
		t.Errorf("reading the key: %+v", a)
	}
}

// Codex can't ask: a patch or a shell command that changes the key is
// refused.
func TestHookCodexSigningKeyRefused(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	_, key := signingKeyFile(t)
	refused := func(what string, o codexOutput) {
		t.Helper()
		h := o.HookSpecificOutput
		if h.PermissionDecision != "deny" || !strings.Contains(h.PermissionDecisionReason, "does not let Codex change it") {
			t.Errorf("%s: %+v, want a refusal", what, h)
		}
	}
	for _, patch := range []string{
		"*** Begin Patch\n*** Add File: " + key + "\n+00\n*** End Patch",
		"*** Begin Patch\n*** Delete File: " + key + "\n*** End Patch",
	} {
		o, _ := codexHook(t, codexPayload(t, "pre-patch-add", root, patch))
		refused(patch, o)
	}
	// Bash and PowerShell read it alike.
	shell := "echo x > '" + key + "'"
	o, _ := codexHook(t, codexPayload(t, "pre-bash-powershell", root, shell))
	refused(shell, o)
}

// A link to the key, or to its folder, leads a write to it.
func TestHookAsksForTheSigningKeyThroughALink(t *testing.T) {
	root := project(t, hookRules)
	clock(t, time.Now())
	dir, _ := signingKeyFile(t)
	l := filepath.Join(t.TempDir(), "cfg")
	link(t, dir, l, true)
	f := filepath.Join(l, "key")
	if o, out := hook(t, inSession("Write", f, root)); !asks(t, o, filepath.ToSlash(f)) {
		t.Errorf("a Write through a link to the key's folder: %s, want ask", out)
	}
}
