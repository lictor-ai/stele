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

	"github.com/lictor-ai/stele/internal/adapter"
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
func runHook(args []string, stdout, _ io.Writer) (code int) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return exitOK
	}
	ev, err := claudecode.Parse(data)
	if err != nil || (ev.File == "" && ev.Command == "" && !ev.Kind.Starts()) {
		return exitOK
	}
	// Go exits 2 on a panic, which would block the write: a bug in stele
	// lets it through with a warning instead.
	defer func() {
		if r := recover(); r != nil {
			w := fmt.Sprintf("stele: rules not checked for this write: internal error: %v", r)
			stdout.Write(claudecode.Render(ev.Name, claudecode.Answer{Warning: w}))
			code = exitOK
		}
	}()
	// The switches stele init wrote on the command (ADR-0006).
	sw := claudecode.HookSwitches(args)
	var a claudecode.Answer
	switch {
	case ev.Kind == adapter.PreShell:
		a = beforeBash(ev, sw)
	case ev.Kind == adapter.PreTool:
		a = beforeWrite(ev, sw)
		a.Warning = joinLines(a.Warning, versionWarning(ev, claudecode.InstalledVersion(args)))
	case ev.Kind == adapter.PostTool && ev.Writes:
		a = afterWrite(ev, sw)
		room := install.MaxMessage
		if a.Context != "" {
			room -= len(a.Context) + len("\n\n") // joinNotes' separator
		}
		w, c := invalidRules(ev, room)
		a.Warning = joinLines(a.Warning, w)
		a.Context = joinNotes(a.Context, c)
	case ev.Kind.Starts():
		a = sessionStart(ev)
	default:
		return exitOK
	}
	if out := claudecode.Render(ev.Name, a); out != nil {
		stdout.Write(out)
	}
	return exitOK
}

// target returns the stele root for file, which a tool writes, and the file
// relative to it. ok is false when the file is in no stele project.
func target(file string) (root, rel string, ok bool) {
	// The root is found from the file, not from where the agent runs, so a
	// subproject with its own stele.md gets its own rules (ADR-0004).
	root, err := findRoot(filepath.Dir(file))
	if err != nil || !usesStele(root) {
		return "", "", false // not a stele project: write nothing, enforce nothing
	}
	rel, err = filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return root, filepath.ToSlash(rel), true
}

// beforeWrite decides a PreToolUse call. With --remind=off only the asks
// for stele's own files and settings are left.
func beforeWrite(ev claudecode.Event, sw claudecode.Switches) claudecode.Answer {
	root, rel, ok := target(ev.File)
	if !ok {
		return askOutside(ev)
	}
	// Before stele.json is read: it is one of the files that would change.
	if ev.Writes && (guarded(root, rel, ev.File) || loadsSettings(ev, ev.File)) {
		return claudecode.Answer{Ask: install.AskMessage(showPath(rel))}
	}
	if sw.RemindOff {
		return claudecode.Answer{}
	}

	policy, perr := enforce.Load(root)
	fail := func(err error) claudecode.Answer { return failure(policy, ev.Kind, err) }
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
			rules = append(rules, backstopRule(e))
		}
	}
	if len(rules) == 0 {
		return claudecode.Answer{Context: note}
	}

	// Decide inside the update, so the rules shown are saved before the deny
	// is answered: a deny that was not recorded would repeat forever.
	var d backstop.Decision
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		in := backstop.Input{PromptID: ev.PromptID, AgentID: ev.AgentID, File: rel, Rules: rules, Now: now(),
			Fit: func(rs []backstop.Rule) int {
				// At least one: a rule too long for any message would
				// otherwise be denied forever.
				return max(1, install.FitRules(len(rs), install.MaxMessage, func(k int) string {
					return install.BlockMessageMore(rel, blockRules(rs[:k]), len(rs)-k, note)
				}))
			}}
		d, *st = backstop.Decide(in, *st)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if !d.Deny {
		return claudecode.Answer{Context: note}
	}
	return claudecode.Answer{Deny: true, Reason: install.BlockMessageMore(rel, blockRules(d.Rules), d.More, note)}
}

// blockRules is rules as a message shows them.
func blockRules(rules []backstop.Rule) []install.BlockRule {
	out := make([]install.BlockRule, len(rules))
	for i, r := range rules {
		out[i] = install.BlockRule{ID: r.ID, Text: r.Text}
	}
	return out
}

// usesStele reports whether root has stele.md or stele.json. findRoot stops
// at a .git, and a repository that does not use stele must not get a
// stele.lock or a .stele/ from a hook registered user-wide.
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
// fixed (ADR-0004). A rules file that cannot be written is no failure: the
// index is saved and the hook still shows every rule.
func freshIndex(root string, key []byte) (idx index.Index, note string, err error) {
	idx, _, err = compile.EnsureFresh(context.Background(), root, plugin.Builtins(), generated(), uuid.NewString, key)
	var ge *compile.GeneratedError
	if errors.As(err, &ge) {
		return idx, "", nil
	}
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
func afterWrite(ev claudecode.Event, sw claudecode.Switches) claudecode.Answer {
	root, rel, ok := target(ev.File)
	if !ok {
		return claudecode.Answer{}
	}
	if sw.CheckOff {
		return refreshOnly(root)
	}

	policy, perr := enforce.Load(root)
	fail := func(err error) claudecode.Answer { return failure(policy, ev.Kind, err) }
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
	if !ev.Whole {
		spans := written(ev, content)
		if len(spans) == 0 {
			return claudecode.Answer{Context: note} // a deletion, or text not found
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
	message := func(vs []install.Violation, blocking bool, limit int) string {
		if len(vs) == 0 {
			return ""
		}
		n := more + max(0, len(vs)-maxShown)
		more = 0
		return install.ViolationMessageWithin(rel, vs[:min(len(vs), maxShown)], n, blocking, limit)
	}
	// The blocking violations are the block's reason; the context holds the
	// notes and the non-blocking ones, which share its budget.
	var a claudecode.Answer
	if len(hard) > 0 {
		a.Deny, a.Reason = true, message(hard, true, install.MaxMessage)
	}
	budget := install.MaxMessage - len(joinNotes(notes...))
	softMsg := message(soft, false, budget-4)
	a.Context = joinNotes(append([]string{softMsg}, notes...)...)
	return a
}

// refreshOnly is the post-write answer with --check=off. Nothing is checked,
// but the index and the rules file are rebuilt when stele.md changed: only
// this hook compiles an agent's edit to stele.md, whose write asks before it
// runs (ADR-0006). A failure only warns: no check was asked for, so there is
// nothing for block_on_error to stand in for.
func refreshOnly(root string) claudecode.Answer {
	warn := func(err error) claudecode.Answer {
		return claudecode.Answer{Warning: fmt.Sprintf("stele: rules not refreshed after this write: %v", err)}
	}
	key, err := loadKey()
	if err != nil {
		return warn(err)
	}
	_, note, err := freshIndex(root, key)
	if err != nil {
		return warn(err)
	}
	return claudecode.Answer{Context: note}
}

// written returns the lines an Edit or MultiEdit wrote: exactly, from
// Claude Code's patch, or else from where its new text is in the file.
func written(ev claudecode.Event, content []byte) []check.Span {
	if ev.Patched {
		spans := make([]check.Span, len(ev.Added))
		for i, l := range ev.Added {
			spans[i] = check.Span{First: l.First, Last: l.Last}
		}
		return check.Merge(spans)
	}
	fs := make([]check.Fragment, len(ev.NewText))
	for i, t := range ev.NewText {
		fs[i] = check.Fragment{Text: t, ReplaceAll: i < len(ev.ReplaceAll) && ev.ReplaceAll[i]}
	}
	return check.Spans(content, fs)
}

// versionWarning tells the user, once a session, that the hooks were
// installed by another stele than the one running (spec §6). Every write
// passes through PreToolUse, so the post-write path does not repeat it. A
// matching version costs a string compare; only a mismatch reads anything.
func versionWarning(ev claudecode.Event, installed string) string {
	// Only two different releases warn: a build from a commit, or with local
	// changes, stamps devel and is never told to re-run init (#41).
	running := binaryVersion()
	if installed == running || !install.Released(installed) || !install.Released(running) {
		return ""
	}
	root, _, ok := target(ev.File)
	if !ok {
		return ""
	}
	msg := fmt.Sprintf("stele: the hooks in %s were installed by stele %s; this is stele %s. Run stele init to update them.",
		filepath.ToSlash(claudecode.SettingsFile), installed, running)
	if install.Older(running, installed) {
		// An older init takes hooks carrying switches for hand-written ones
		// and adds a second set (ADR-0006): it must not be run.
		msg = fmt.Sprintf("stele: the hooks in %s were installed by stele %s; this is stele %s, which is older. Upgrade stele; do not run stele init with it.",
			filepath.ToSlash(claudecode.SettingsFile), installed, running)
	}
	key, err := loadKey()
	if err != nil {
		return msg
	}
	mark, warn := installed+" "+running, true
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		warn = st.VersionWarned != mark
		st.VersionWarned = mark
		return nil
	})
	if err != nil || warn {
		return msg // a warning too many beats none
	}
	return ""
}

// joinLines joins the non-empty lines.
func joinLines(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n" + b
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
// can be stopped: the agent is told the write went unchecked. Only a part
// the switches leave on gets here (ADR-0006).
func failure(p enforce.Policy, kind adapter.Kind, err error) claudecode.Answer {
	post := kind == adapter.PostTool
	if p.BlockOnError {
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
