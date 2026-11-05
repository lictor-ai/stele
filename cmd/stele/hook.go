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
	"github.com/lictor-ai/stele/internal/backstop"
	"github.com/lictor-ai/stele/internal/check"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/journal"
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

// runHook answers one hook call from the agent (ADR-0004). Before a write, it
// shows the agent the rules for the file it has not seen this turn, by
// denying the write with them as the reason. After a write, it checks the
// lines the agent wrote and sends back the rules they break.
//
// It always exits 0, and prints nothing when it has nothing to say: exit 2
// would block the write whatever the JSON says, and a hook that cannot make
// sense of its input must never stop the agent.
func runHook(args []string, stdout, _ io.Writer) (code int) {
	who, ok := hookAgent(args)
	if !ok {
		return exitOK
	}
	defer func(prev adapter.Adapter) { agent = prev }(agent)
	agent = who
	data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		return exitOK
	}
	ev, err := agent.Parse(data)
	if err != nil || (len(ev.Edits) == 0 && ev.Command == "" && !ev.Kind.Starts()) {
		return exitOK
	}
	// Go exits 2 on a panic, which would block the write: a bug in stele
	// lets it through with a warning instead.
	defer func() {
		if r := recover(); r != nil {
			w := fmt.Sprintf("stele: rules not checked for this write: internal error: %v", r)
			stdout.Write(agent.Render(ev, adapter.Answer{Warning: w}))
			code = exitOK
		}
	}()
	// The version and switches stele init wrote on the command (ADR-0006).
	installed, sw := agent.HookArgs(args)
	var a adapter.Answer
	switch {
	case ev.Kind == adapter.PreShell:
		a = beforeBash(ev, sw)
	case ev.Kind == adapter.PreTool && len(ev.Edits) > 0:
		a = beforeWrite(ev, sw)
		a.Warning = joinLines(a.Warning, versionWarning(ev, ev.Edits[0].File, installed))
	case ev.Kind == adapter.PostTool && len(writes(ev)) > 0:
		a = afterWrites(ev, sw)
		room := install.MaxMessage
		if a.Context != "" {
			room = max(0, room-len(a.Context)-len("\n\n")) // joinNotes' separator
		}
		w, c := invalidRules(ev, rulesEdit(ev).File, room)
		a.Warning = joinLines(a.Warning, w)
		a.Context = joinNotes(a.Context, c)
	case ev.Kind.Starts():
		a = sessionStart(ev)
	default:
		return exitOK
	}
	if out := agent.Render(ev, a); out != nil {
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

// beforeWrite decides a PreTool call. With --remind=off only the asks
// for stele's own files and settings are left. A call that writes several
// files, a patch, gets one answer: an ask for any of stele's own files, else
// one decision per project for the rules of all its files, any deny winning
// (ADR-0012).
func beforeWrite(ev adapter.Event, sw adapter.Switches) adapter.Answer {
	var asks, roots []string
	rels := map[string][]string{}
	for _, e := range ev.Edits {
		touches := e.Writes || e.Removes
		root, rel, ok := target(e.File)
		if !ok {
			if a := askOutside(ev, e); a.Ask != "" {
				asks = append(asks, a.Ask)
			}
			continue
		}
		// Before stele.json is read: it is one of the files that would change.
		if touches && (guarded(root, rel, e.File) || loadsSettings(ev, e.File)) {
			asks = append(asks, install.AskMessage(agent.Words(), showPath(rel)))
			continue
		}
		if e.Removes && !e.Writes {
			continue // a removal shows no rules: nothing is written
		}
		if _, seen := rels[root]; !seen {
			roots = append(roots, root)
		}
		rels[root] = append(rels[root], rel)
	}
	if len(asks) > 0 {
		return adapter.Answer{Ask: joinNotes(asks...)}
	}
	if sw.RemindOff {
		return adapter.Answer{}
	}
	// Each project gets an equal share of the message, and shows at least
	// one rule in it.
	var a adapter.Answer
	budget := install.MaxMessage
	if len(roots) > 1 {
		budget = install.MaxMessage/len(roots) - len("\n\n")
	}
	for _, root := range roots {
		r := beforeRoot(ev, root, rels[root], budget)
		if r.Deny {
			a.Deny, a.Reason = true, joinNotes(a.Reason, r.Reason)
		}
		a.Context = joinNotes(a.Context, r.Context)
		a.Warning = joinLines(a.Warning, r.Warning)
	}
	return a
}

// beforeRoot decides the files, relative to root, that one tool call writes
// in one project: the rules of every file, each once, in the index's order.
// Its deny reason fits in budget bytes.
func beforeRoot(ev adapter.Event, root string, rels []string, budget int) adapter.Answer {
	policy, perr := enforce.Load(root)
	fail := func(err error) adapter.Answer { return failure(policy, ev.Kind, err) }
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
		for _, rel := range rels {
			if scope.Applies(e.Scope, rel) {
				rules = append(rules, backstopRule(e))
				break
			}
		}
	}
	if len(rules) == 0 {
		return adapter.Answer{Context: note}
	}
	// One file is named as it is; several, as a list. The backstop knows a
	// retry by its key: the same files again.
	shown, file := rels[0], rels[0]
	if len(rels) > 1 {
		shown, file = strings.Join(rels, ", "), strings.Join(rels, "\n")
	}

	// Decide inside the update, so the rules shown are saved before the deny
	// is answered: a deny that was not recorded would repeat forever.
	var d backstop.Decision
	_, err = session.Update(root, ev.SessionID, now(), key, func(st *session.State) error {
		in := backstop.Input{PromptID: ev.PromptID, AgentID: ev.AgentID, File: file, Rules: rules, Now: now(),
			Fit: func(rs []backstop.Rule) int {
				// At least one: a rule too long for any message would
				// otherwise be denied forever.
				return max(1, install.FitRules(len(rs), budget, func(k int) string {
					return install.BlockMessageMore(agent.Words(), shown, blockRules(rs[:k]), len(rs)-k, note)
				}))
			}}
		d, *st = backstop.Decide(in, *st)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if !d.Deny {
		return adapter.Answer{Context: note}
	}
	record(root, key, &journal.DeniedRecord{Header: logHeader(idx.Project, ev, ev.AgentID), Prompt: ev.PromptID,
		Via: "write", Files: rels, Rules: ruleIDs(d.Rules), More: d.More})
	return adapter.Answer{Deny: true, Reason: install.BlockMessageMore(agent.Words(), shown, blockRules(d.Rules), d.More, note)}
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
	idx, _, err = compile.EnsureFresh(context.Background(), root, plugin.Builtins(), generated(root), uuid.NewString, key)
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

// writes are the edits of ev that write a file's content.
func writes(ev adapter.Event) []adapter.Edit {
	var out []adapter.Edit
	for _, e := range ev.Edits {
		if e.Writes {
			out = append(out, e)
		}
	}
	return out
}

// rulesEdit is the written edit invalidRules reads: a write to a root's
// stele.md if there is one, so the agent hears about the rule it wrote, else
// the first.
func rulesEdit(ev adapter.Event) adapter.Edit {
	ws := writes(ev)
	for _, e := range ws {
		if _, rel, ok := target(e.File); ok && rel == "stele.md" {
			return e
		}
	}
	return ws[0]
}

// postRoom is the least room one file's check needs in each of the answer's
// messages; a file after those that left less is not checked, and the
// answer says how many were not.
const postRoom = 512

// skippedRoom is what the note on files not checked needs.
const skippedRoom = 200

// afterWrites decides a PostTool call: each file written is checked, and
// the answers are one, any block winning. The block's reasons share one
// message of install.MaxMessage, and the context another, as for one file; a
// project's note is given once.
func afterWrites(ev adapter.Event, sw adapter.Switches) adapter.Answer {
	ws := writes(ev)
	var a adapter.Answer
	reasonRoom, contextCap := install.MaxMessage, install.MaxMessage
	if len(ws) > 1 {
		contextCap -= skippedRoom
	}
	contextRoom := contextCap
	noted := map[string]bool{}
	skipped := 0
	for _, e := range ws {
		if reasonRoom < postRoom || contextRoom < postRoom {
			skipped++
			continue
		}
		r := afterWrite(ev, e, sw, reasonRoom, contextRoom, noted)
		if r.Deny {
			a.Deny, a.Reason = true, joinNotes(a.Reason, r.Reason)
			reasonRoom = install.MaxMessage - len(a.Reason) - len("\n\n")
		}
		a.Context = joinNotes(a.Context, r.Context)
		contextRoom = contextCap - len(a.Context) - len("\n\n")
		if !strings.Contains(a.Warning, r.Warning) {
			a.Warning = joinLines(a.Warning, r.Warning)
		}
	}
	if skipped > 0 {
		a.Context = joinNotes(a.Context, fmt.Sprintf("stele did not check %d more of the files this call wrote: its message was full. Run stele check on them.", skipped))
	}
	return a
}

// afterWrite decides one file a PostTool call wrote: it checks the lines the
// agent just wrote against the rules with a built-in check (D45), the block's
// reason in at most reasonRoom bytes and the context in at most contextRoom.
// A project's note already in noted is not given again. It keeps no session
// state, so parallel writes cannot race.
func afterWrite(ev adapter.Event, e adapter.Edit, sw adapter.Switches, reasonRoom, contextRoom int, noted map[string]bool) adapter.Answer {
	root, rel, ok := target(e.File)
	if !ok {
		return adapter.Answer{}
	}
	if sw.CheckOff {
		return refreshOnly(root, rel, ev, noted)
	}

	policy, perr := enforce.Load(root)
	fail := func(err error) adapter.Answer { return failure(policy, ev.Kind, err) }
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
	note = once(noted, note)
	recordWrote(root, key, idx, ev, rel)

	// The file is read back, not taken from the payload: an Edit carries only
	// its new text, and what is on disk is what stele check will see.
	content, err := safefile.ReadRegular(e.File, maxChecked)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return adapter.Answer{Context: note} // removed since: nothing to check
	case errors.Is(err, safefile.ErrNotRegular):
		return adapter.Answer{Context: joinNotes(note, unchecked(rel, "not a regular file"))}
	case errors.Is(err, safefile.ErrTooLarge):
		return adapter.Answer{Context: joinNotes(note, unchecked(rel, fmt.Sprintf("larger than %d bytes", maxChecked)))}
	case err != nil:
		return fail(err)
	}

	// Only the agent's lines count: older violations are left to stele check.
	var keep func(int) bool
	if !e.Whole {
		spans := written(e, content)
		if len(spans) == 0 {
			return adapter.Answer{Context: note} // a deletion, or text not found
		}
		keep = func(line int) bool { return check.Contain(spans, line) }
	}
	// A file stele generated for the agent holds every rule's text: it is
	// never checked, as check.RunFileLines skips stele's other files.
	var r check.Report
	if !own(rel) {
		var ids []string
		r, ids = check.RunFileChecked(idx, rel, content, plugin.BuiltinCheckers(), keep)
		if len(ids) > 0 {
			rec := &journal.CheckedRecord{Header: logHeader(idx.Project, ev, ev.AgentID), Prompt: ev.PromptID,
				File: rel, Rules: ids, More: r.Truncated}
			for _, f := range r.Violations {
				rec.Violations = append(rec.Violations, journal.Violation{Rule: f.RuleID, Line: f.Line, Blocking: f.Blocking})
			}
			record(root, key, rec)
		}
	}

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
		return install.ViolationMessageWithin(agent.Words(), rel, vs[:min(len(vs), maxShown)], n, blocking, limit)
	}
	// The blocking violations are the block's reason; the context holds the
	// notes and the non-blocking ones, which share its budget.
	var a adapter.Answer
	if len(hard) > 0 {
		a.Deny, a.Reason = true, message(hard, true, reasonRoom)
	}
	room := contextRoom - len(joinNotes(notes...))
	softMsg := ""
	if room-4 > 0 {
		softMsg = message(soft, false, room-4)
	}
	a.Context = joinNotes(append([]string{softMsg}, notes...)...)
	return a
}

// once returns note the first time noted sees it, and "" after: one tool
// call's files in one project share their project's note.
func once(noted map[string]bool, note string) string {
	if note == "" || noted == nil {
		return note
	}
	if noted[note] {
		return ""
	}
	noted[note] = true
	return note
}

// refreshOnly is the post-write answer with --check=off. Nothing is checked,
// but the index and the rules file are rebuilt when stele.md changed: only
// this hook compiles an agent's edit to stele.md, whose write asks before it
// runs (ADR-0006). A failure only warns: no check was asked for, so there is
// nothing for block_on_error to stand in for.
func refreshOnly(root, rel string, ev adapter.Event, noted map[string]bool) adapter.Answer {
	warn := func(err error) adapter.Answer {
		return adapter.Answer{Warning: fmt.Sprintf("stele: rules not refreshed after this write: %v", err)}
	}
	key, err := loadKey()
	if err != nil {
		return warn(err)
	}
	idx, note, err := freshIndex(root, key)
	if err != nil {
		return warn(err)
	}
	recordWrote(root, key, idx, ev, rel)
	return adapter.Answer{Context: once(noted, note)}
}

// written returns the lines an Edit or MultiEdit wrote: exactly, from
// the agent's patch, or else from where its new text is in the file.
func written(e adapter.Edit, content []byte) []check.Span {
	if e.Patched {
		spans := make([]check.Span, len(e.Added))
		for i, l := range e.Added {
			spans[i] = check.Span{First: l.First, Last: l.Last}
		}
		return check.Merge(spans)
	}
	fs := make([]check.Fragment, len(e.NewText))
	for i, t := range e.NewText {
		fs[i] = check.Fragment{Text: t, ReplaceAll: i < len(e.ReplaceAll) && e.ReplaceAll[i]}
	}
	return check.Spans(content, fs)
}

// versionWarning tells the user, once a session, that the hooks were
// installed by another stele than the one running (spec §6). Every write
// passes through PreTool, so the post-write path does not repeat it. A
// matching version costs a string compare; only a mismatch reads anything.
// file is a file the call touches, which finds the project.
func versionWarning(ev adapter.Event, file, installed string) string {
	// Only two different releases warn: a build from a commit, or with local
	// changes, stamps devel and is never told to re-run init (#41).
	running := binaryVersion()
	if installed == running || !install.Released(installed) || !install.Released(running) {
		return ""
	}
	root, _, ok := target(file)
	if !ok {
		return ""
	}
	msg := fmt.Sprintf("stele: the hooks in %s were installed by stele %s; this is stele %s. Run stele init to update them.",
		filepath.ToSlash(agent.SettingsFile()), installed, running)
	if install.Older(running, installed) {
		// An older init takes hooks carrying switches for hand-written ones
		// and adds a second set (ADR-0006): it must not be run.
		msg = fmt.Sprintf("stele: the hooks in %s were installed by stele %s; this is stele %s, which is older. Upgrade stele; do not run stele init with it.",
			filepath.ToSlash(agent.SettingsFile()), installed, running)
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
func failure(p enforce.Policy, kind adapter.Kind, err error) adapter.Answer {
	post := kind == adapter.PostTool
	if p.BlockOnError {
		if post {
			return adapter.Answer{Deny: true, Reason: fmt.Sprintf("stele could not check this write against the project's rules: %v. block_on_error is on in stele.json, so the write is reported as unchecked: it was saved, but fix stele before going on.", err)}
		}
		return adapter.Answer{Deny: true, Reason: fmt.Sprintf("stele could not check this write against the project's rules: %v. block_on_error is on in stele.json, so the write is blocked until this is fixed.", err)}
	}
	if post {
		return adapter.Answer{Warning: fmt.Sprintf("stele: rules not checked for this write: %v", err)}
	}
	return adapter.Answer{Warning: fmt.Sprintf("stele: rules not enforced for this write: %v", err)}
}
