package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/adapter/claudecode"
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/safefile"
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

// maxChecked caps the file the post-write check reads, as the regex checker
// caps the files stele check reads. Tests lower it.
var maxChecked int64 = 32 << 20

// maxShown caps the violations one message shows; the rest are counted.
const maxShown = 20

// runHook answers one Claude Code hook call (ADR-0004). Before a write, it
// shows the agent the rules for the file it has not seen this turn, by
// denying the write with them as the reason. After a write, it checks the
// lines the agent wrote and sends back the rules they break.
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
	if err != nil || ev.File == "" {
		return exitOK
	}
	var a claudecode.Answer
	switch {
	case ev.Name == "PreToolUse":
		a = beforeWrite(ev)
	case ev.Name == "PostToolUse" && writes[ev.ToolName]:
		a = afterWrite(ev)
	default:
		return exitOK
	}
	if out := claudecode.Render(ev.Name, a); out != nil {
		stdout.Write(out)
	}
	return exitOK
}

// writes are the tools whose result the post-write check reads. A Read also
// names a file_path.
var writes = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true}

// target returns the stele root for the file ev writes, and the file
// relative to it. ok is false when the file is in no stele project.
func target(ev claudecode.Event) (root, rel string, ok bool) {
	// The root is found from the file, not from where the agent runs, so a
	// subproject with its own stele.md gets its own rules (ADR-0004).
	root, err := findRoot(filepath.Dir(ev.File))
	if err != nil || !usesStele(root) {
		return "", "", false // not a stele project: write nothing, enforce nothing
	}
	rel, err = filepath.Rel(root, ev.File)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return root, filepath.ToSlash(rel), true
}

// beforeWrite decides a PreToolUse call.
func beforeWrite(ev claudecode.Event) claudecode.Answer {
	root, rel, ok := target(ev)
	if !ok {
		return claudecode.Answer{}
	}

	policy, perr := enforce.Load(root)
	if !policy.Backstop {
		return claudecode.Answer{}
	}
	fail := func(err error) claudecode.Answer { return failure(policy, ev.Name, err) }
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

// afterWrite decides a PostToolUse call: it checks the lines the agent just
// wrote against the rules with a built-in check (D45). It keeps no session
// state, so parallel writes cannot race.
func afterWrite(ev claudecode.Event) claudecode.Answer {
	root, rel, ok := target(ev)
	if !ok {
		return claudecode.Answer{}
	}

	policy, perr := enforce.Load(root)
	fail := func(err error) claudecode.Answer { return failure(policy, ev.Name, err) }
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

	// The file is read back, not taken from the payload: an Edit carries only
	// its new text, and what is on disk is what stele check will see.
	content, err := safefile.ReadRegular(ev.File, maxChecked)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return claudecode.Answer{Context: note} // removed since: nothing to check
	case errors.Is(err, safefile.ErrNotRegular):
		return claudecode.Answer{Context: joinNotes(note, unchecked(rel, "not a regular file"))}
	case errors.Is(err, safefile.ErrTooLarge):
		return claudecode.Answer{Context: joinNotes(note, unchecked(rel, fmt.Sprintf("larger than %d bytes", maxChecked)))}
	case err != nil:
		return fail(err)
	}

	// Only the agent's lines count: older violations are left to stele check.
	var keep func(int) bool
	if ev.ToolName != "Write" {
		spans := check.Spans(content, ev.NewText)
		if len(spans) == 0 {
			return claudecode.Answer{Context: note} // a deletion, or text since changed
		}
		keep = func(line int) bool { return check.Contain(spans, line) }
	}
	r := check.RunFileLines(idx, rel, content, plugin.BuiltinCheckers(), keep)

	var hard, soft []install.Violation
	for _, f := range r.Violations {
		v := install.Violation{Line: f.Line, Column: f.Column, RuleID: f.RuleID, Excerpt: f.Excerpt, Message: f.Message}
		if f.Blocking {
			hard = append(hard, v)
		} else {
			soft = append(soft, v)
		}
	}
	notes := []string{note}
	for _, s := range r.Skipped {
		notes = append(notes, unchecked(rel, s.Reason))
	}
	// Violations past the cap per rule cannot be told blocking or not; they
	// are counted with the first message shown.
	more := r.Truncated
	message := func(vs []install.Violation, blocking bool) string {
		if len(vs) == 0 {
			return ""
		}
		n := more + max(0, len(vs)-maxShown)
		more = 0
		return install.ViolationMessage(rel, vs[:min(len(vs), maxShown)], n, blocking)
	}
	var a claudecode.Answer
	hardMsg := message(hard, true)
	softMsg := message(soft, false)
	if hardMsg != "" && policy.Backstop {
		a.Deny, a.Reason = true, hardMsg
		hardMsg = ""
	}
	// With backstop off the hook never blocks (ADR-0004): the agent is told.
	a.Context = joinNotes(append([]string{hardMsg, softMsg}, notes...)...)
	return a
}

// unchecked is the note for a file the post-write check could not read.
func unchecked(rel, reason string) string {
	return fmt.Sprintf("stele did not check %s: %s.", rel, reason)
}

// joinNotes joins the non-empty notes into one context.
func joinNotes(notes ...string) string {
	var kept []string
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, "\n\n")
}

// failure is the answer when stele itself cannot run. With block_on_error off,
// the default, the write goes through and the user is warned; with it on, the
// write is blocked and the agent told why (ADR-0004). After a write, nothing
// can be stopped: the agent is told the write went unchecked. With backstop
// off the hook never blocks, so only the warning is left.
func failure(p enforce.Policy, event string, err error) claudecode.Answer {
	post := event == "PostToolUse"
	if p.BlockOnError && p.Backstop {
		if post {
			return claudecode.Answer{Deny: true, Reason: fmt.Sprintf("stele could not check this write against the project's rules: %v. block_on_error is on in stele.json, so the write is reported as unchecked: it was saved, but fix stele before going on.", err)}
		}
		return claudecode.Answer{Deny: true, Reason: fmt.Sprintf("stele could not check this write against the project's rules: %v. block_on_error is on in stele.json, so the write is blocked until this is fixed.", err)}
	}
	if post {
		return claudecode.Answer{Warning: fmt.Sprintf("stele: rules not checked for this write: %v", err)}
	}
	return claudecode.Answer{Warning: fmt.Sprintf("stele: rules not enforced for this write: %v", err)}
}
