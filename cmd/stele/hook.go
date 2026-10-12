package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/scope"
	"github.com/lictor-ai/stele/internal/session"
	"github.com/lictor-ai/stele/internal/source"
)

// stdin and now are the hook's inputs from the world; tests replace them.
var (
	stdin io.Reader = os.Stdin
	now             = time.Now
)

// maxPayload caps the hook payload. A Write carries the whole file, so it can
// be large, but not unbounded.
const maxPayload = 64 << 20

// runHook answers one Claude Code hook call (ADR-0004). Before a write, it
// shows the agent the rules for the file it has not seen this turn, by
// denying the write with them as the reason.
//
// It always exits 0, and prints nothing when it has nothing to say: exit 2
// would block the write whatever the JSON says, and a hook that cannot make
// sense of its input must never stop the agent.
func runHook(_ []string, stdout, _ io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return exitOK
	}
	ev, err := claudecode.Parse(data)
	if err != nil || ev.Name != "PreToolUse" || ev.File == "" {
		return exitOK // the post-write check is #29
	}
	if out := claudecode.Render(ev.Name, beforeWrite(ev)); out != nil {
		stdout.Write(out)
	}
	return exitOK
}

// beforeWrite decides a PreToolUse call.
func beforeWrite(ev claudecode.Event) claudecode.Answer {
	// The root is found from the file, not from where the agent runs, so a
	// subproject with its own stele.md gets its own rules (ADR-0004).
	root, err := findRoot(filepath.Dir(ev.File))
	if err != nil || !usesStele(root) {
		return claudecode.Answer{} // not a stele project: write nothing, enforce nothing
	}
	rel, err := filepath.Rel(root, ev.File)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return claudecode.Answer{}
	}
	rel = filepath.ToSlash(rel)

	policy, perr := enforce.Load(root)
	if !policy.Backstop {
		return claudecode.Answer{}
	}
	fail := func(err error) claudecode.Answer { return failure(policy, err) }
	if perr != nil {
		return fail(perr)
	}

	key, err := loadKey()
	if err != nil {
		return fail(err)
	}
	idx, note, err := freshIndex(root, key)
	if err != nil {
		return fail(err)
	}

	var rules []backstop.Rule
	for _, e := range idx.Rules {
		if scope.Applies(e.Scope, rel) {
			rules = append(rules, backstop.Rule{ID: e.ID, Text: e.Body})
		}
	}
	if len(rules) == 0 {
		return claudecode.Answer{Context: note}
	}

	// Decide inside the update, so the rules shown are saved before the deny
	// is answered: a deny that was not recorded would repeat forever.
	var d backstop.Decision
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		in := backstop.Input{PromptID: ev.PromptID, AgentID: ev.AgentID, Rules: rules, Now: now()}
		d, *st = backstop.Decide(in, *st, policy)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if !d.Deny {
		return claudecode.Answer{Context: note}
	}
	shown := make([]install.BlockRule, len(d.Rules))
	for i, r := range d.Rules {
		shown[i] = install.BlockRule{ID: r.ID, Text: r.Text}
	}
	return claudecode.Answer{Deny: true, Reason: install.BlockMessage(rel, shown, note)}
}

// usesStele reports whether root has stele.md or stele.json. findRoot falls
// back to a .git directory, and a repository that does not use stele must not
// get a stele.lock or a .stele/ from a hook registered user-wide.
func usesStele(root string) bool {
	for _, name := range []string{"stele.md", enforce.FileName} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return true
		}
	}
	return false
}

// freshIndex returns the index, rebuilt from stele.md when it changed. When
// stele.md stops parsing, the last good index stays in force and note says
// what is wrong, so the rules are still enforced while the file is being
// fixed (ADR-0004).
func freshIndex(root string, key []byte) (idx index.Index, note string, err error) {
	idx, _, err = compile.EnsureFresh(context.Background(), root, plugin.Builtins(), uuid.NewString, key)
	var pe *source.ParseError
	if !errors.As(err, &pe) {
		return idx, "", err
	}
	last, lerr := index.Load(root, key)
	if lerr != nil {
		return index.Index{}, "", fmt.Errorf("%w, and there is no earlier valid index to fall back on", err)
	}
	return last, fmt.Sprintf("%v. The last valid rules still apply; fix stele.md for your changes to it to take effect.", err), nil
}

// failure is the answer when stele itself cannot run. With block_on_error off,
// the default, the write goes through and the user is warned; with it on, the
// write is blocked and the agent told why (ADR-0004).
func failure(p enforce.Policy, err error) claudecode.Answer {
	if p.BlockOnError {
		return claudecode.Answer{Deny: true, Reason: fmt.Sprintf("stele could not check this write against the project's rules: %v. block_on_error is on in stele.json, so the write is blocked until this is fixed.", err)}
	}
	return claudecode.Answer{Warning: fmt.Sprintf("stele: rules not enforced for this write: %v", err)}
}
