package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/jsonedit"
)

// SettingsFile is where init registers the hooks, relative to the root: the
// project's hooks file, which Codex reads in a trusted project.
var SettingsFile = filepath.Join(".codex", "hooks.json")

// configFile is the project's other Codex file, which may hold hooks too.
const configFile = ".codex/config.toml"

// registration is one hook init registers: an event, and the matcher of its
// group, "" for an event that takes none. Codex matches a regex on the tool's
// name.
type registration struct{ Event, Matcher string }

// registrations are the hooks init registers: before a patch or a shell
// command, after a patch, and when a session starts.
var registrations = []registration{
	{"PreToolUse", "^(apply_patch|Bash)$"},
	{"PostToolUse", "^apply_patch$"},
	{"SessionStart", ""},
}

// events are the events of registrations, in order.
var events = func() []string {
	var es []string
	for _, r := range registrations {
		es = append(es, r.Event)
	}
	return es
}()

// agentArg names Codex on the command init writes: stele hook answers the
// agent it names (ADR-0012).
const agentArg = "--agent=codex"

// The switch flags. A switch that is on is never written.
const (
	remindOff = "--remind=off"
	checkOff  = "--check=off"
)

// flags are the words init writes after the agent, in this order.
func flags(s adapter.Switches) []string {
	var f []string
	if s.RemindOff {
		f = append(f, remindOff)
	}
	if s.CheckOff {
		f = append(f, checkOff)
	}
	return f
}

// command is the command init writes for the switches. It carries no
// version: Codex asks the user to approve a hook again whenever its command
// changes, and an upgrade of stele must not switch its hooks off.
func command(sw adapter.Switches) string {
	return strings.Join(append([]string{"stele", "hook", agentArg}, flags(sw)...), " ")
}

// handler is one hook handler, as much of it as stele reads.
type handler struct {
	Command string `json:"command"`
}

// words is the handler's command line, split on spaces outside quotes.
func (h handler) words() []string { return split(h.Command) }

// split splits a command line on spaces, keeping what is inside single or
// double quotes together, quotes removed.
func split(s string) []string {
	var words []string
	var w strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

// runsStele reports whether the handler runs stele hook, in any form.
func (h handler) runsStele() bool {
	w := h.words()
	if len(w) < 2 || w[1] != "hook" {
		return false
	}
	name := path.Base(strings.ReplaceAll(w[0], `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(name), ".exe") == "stele"
}

// stamp returns the switches on a handler init wrote: exactly stele hook
// --agent=codex, then the flags of the switches that are off, in init's
// order. ok is false for any other handler, a stele hook written by hand
// included.
func (h handler) stamp() (sw adapter.Switches, ok bool) {
	w := h.words()
	if !h.runsStele() || len(w) < 3 || w[2] != agentArg {
		return adapter.Switches{}, false
	}
	sw = HookSwitches(w[3:])
	if !slices.Equal(w[3:], flags(sw)) {
		return adapter.Switches{}, false
	}
	return sw, true
}

// HookSwitches reads the switches from stele hook's own arguments. The last
// of --remind=on|off wins, likewise --check; anything else is ignored.
func HookSwitches(args []string) adapter.Switches {
	var sw adapter.Switches
	for _, a := range args {
		switch a {
		case remindOff:
			sw.RemindOff = true
		case "--remind=on":
			sw.RemindOff = false
		case checkOff:
			sw.CheckOff = true
		case "--check=on":
			sw.CheckOff = false
		}
	}
	return sw
}

// approval is init's note whenever it writes Codex's hooks.
const approval = "Codex runs a project's hooks only in a trusted project, and only once you approve each one: " +
	"start codex here, trust the project, type /hooks and approve stele's hooks. Until then Codex skips them, " +
	"without a word in codex exec. Codex asks again whenever stele init changes them (--remind, --check)."

// Install returns settings with stele's hooks on every event of
// registrations, carrying sw. Empty settings are {}. changed is false when
// each event already has exactly one stele handler, with these switches, in
// a group with the registered matcher. Otherwise stele's handler is replaced
// where it is, so the hooks around it keep their places, which Codex's
// approvals are keyed by; one that is missing is appended in a group of its
// own, after the user's. notes name stele hooks written by hand, left alone,
// and say to approve the hooks. Malformed settings are an error, never
// rewritten.
func Install(settings []byte, sw adapter.Switches) (out []byte, changed bool, notes []string, err error) {
	f, err := parseSettings(settings)
	if err != nil {
		return nil, false, nil, err
	}
	for _, r := range registrations {
		var current []bool
		for _, g := range f.groups[r.Event] {
			for _, h := range g.handlers {
				if s, ok := h.stamp(); ok {
					current = append(current, g.matcher() == r.Matcher && s == sw)
				} else if h.runsStele() {
					notes = append(notes, fmt.Sprintf("%s already runs a stele hook stele init did not write; it is left alone, so stele will run twice", r.Event))
				}
			}
		}
		if len(current) != 1 || !current[0] {
			changed = true
		}
	}
	if !changed {
		return settings, false, notes, nil
	}
	fresh, err := jsonedit.Marshal(struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}{"command", command(sw), 10})
	if err != nil {
		return nil, false, nil, err
	}
	for _, r := range registrations {
		if err := f.place(r, fresh); err != nil {
			return nil, false, nil, err
		}
	}
	out, err = f.render()
	if err != nil {
		return nil, false, nil, err
	}
	return out, true, append(notes, approval), nil
}

// place puts fresh, stele's handler, on r's event: in place of the first one
// init wrote in a group with r's matcher, the others removed; or, when there
// is none, in a new group at the end.
func (f *settingsFile) place(r registration, fresh json.RawMessage) error {
	done := false
	for _, g := range f.groups[r.Event] {
		for i, h := range g.handlers {
			if _, ok := h.stamp(); !ok {
				continue
			}
			if !done && g.matcher() == r.Matcher {
				g.hooks[i], done = fresh, true
				g.handlers[i] = handler{Command: "stele hook " + agentArg} // stele's, already placed
				g.dirty = true
				continue
			}
			g.drop[i] = true
		}
	}
	if !done {
		raw, err := jsonedit.Marshal(struct {
			Matcher string `json:"matcher,omitempty"`
			Hooks   []any  `json:"hooks"`
		}{r.Matcher, []any{json.RawMessage(fresh)}})
		if err != nil {
			return err
		}
		f.groups[r.Event] = append(f.groups[r.Event], &group{raw: raw, drop: map[int]bool{}})
	}
	return f.prune(r.Event)
}

// Uninstall returns settings without the handlers Install wrote. A group, an
// event or "hooks" goes only when stele's removal left it empty. empty
// reports that nothing at all is left. With nothing to remove, out is
// settings unchanged.
func Uninstall(settings []byte) (out []byte, removed int, empty bool, err error) {
	f, err := parseSettings(settings)
	if err != nil {
		return nil, 0, false, err
	}
	for _, ev := range events {
		for _, g := range f.groups[ev] {
			for i, h := range g.handlers {
				if _, ok := h.stamp(); ok {
					g.drop[i] = true
					removed++
				}
			}
		}
		if err := f.prune(ev); err != nil {
			return nil, 0, false, err
		}
	}
	if removed == 0 {
		return settings, 0, false, nil
	}
	out, err = f.render()
	if err != nil {
		return nil, 0, false, err
	}
	return out, removed, len(f.top) == 0, nil
}

// Installed returns the switches on stele's hooks.
func Installed(settings []byte) (sw adapter.Switches, ok bool) {
	f, err := parseSettings(settings)
	if err != nil {
		return adapter.Switches{}, false
	}
	for _, ev := range events {
		for _, g := range f.groups[ev] {
			for _, h := range g.handlers {
				if s, ok := h.stamp(); ok {
					return s, true
				}
			}
		}
	}
	return adapter.Switches{}, false
}

// OtherSteleHook reports whether settings, the hooks file or the project's
// config.toml, run stele hook other than as init writes it for sw. A
// config.toml is not read as TOML: any stele hook in it counts.
func OtherSteleHook(settings []byte, sw adapter.Switches) bool {
	var s struct {
		Hooks map[string][]struct {
			Hooks []handler `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(bytes.TrimPrefix(settings, bom), &s) != nil {
		return bytes.Contains(settings, []byte("stele hook")) || bytes.Contains(settings, []byte("stele.exe hook"))
	}
	for _, groups := range s.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if h.runsStele() && h.Command != command(sw) {
					return true
				}
			}
		}
	}
	return false
}

var bom = []byte("\xef\xbb\xbf")

// group is one matcher group of an event, decoded only as far as finding
// stele's handlers in it.
type group struct {
	raw      json.RawMessage // as written, or rebuilt after a change
	fields   jsonedit.Object
	hooks    []json.RawMessage
	handlers []handler    // hooks, decoded
	drop     map[int]bool // hooks to remove
	dirty    bool         // hooks changed in place
}

// matcher returns the group's matcher, "" when it has none or it is not a
// string.
func (g *group) matcher() string {
	raw, ok := g.fields.Get("matcher")
	if !ok {
		return ""
	}
	var m string
	json.Unmarshal(raw, &m)
	return m
}

// settingsFile is a parsed hooks file.
type settingsFile struct {
	doc     jsonedit.Doc
	top     jsonedit.Object
	hooks   jsonedit.Object
	groups  map[string][]*group // the events stele edits
	present map[string]bool     // events the file has
}

func parseSettings(data []byte) (*settingsFile, error) {
	f := &settingsFile{groups: map[string][]*group{}, present: map[string]bool{}}
	doc, empty := jsonedit.Split(data)
	f.doc = doc
	if empty {
		return f, nil
	}
	if err := doc.Valid(); err != nil {
		return nil, err
	}
	top, err := jsonedit.ParseObject(doc.Body, "the hooks file")
	if err != nil {
		return nil, err
	}
	f.top = top
	raw, ok := top.Get("hooks")
	if !ok {
		return f, nil
	}
	if f.hooks, err = jsonedit.ParseObject(raw, `"hooks"`); err != nil {
		return nil, err
	}
	for _, ev := range events {
		raw, ok := f.hooks.Get(ev)
		if !ok {
			continue
		}
		f.present[ev] = true
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			return nil, fmt.Errorf("%q is not an array of matcher groups", ev)
		}
		for _, g := range groups {
			pg, err := parseGroup(g, ev)
			if err != nil {
				return nil, err
			}
			f.groups[ev] = append(f.groups[ev], pg)
		}
	}
	return f, nil
}

// parseGroup reads one matcher group. A group that is not an object is left
// as it is: it holds no handler stele could have written.
func parseGroup(raw json.RawMessage, ev string) (*group, error) {
	g := &group{raw: raw, drop: map[int]bool{}}
	fields, err := jsonedit.ParseObject(raw, "a "+ev+" group")
	if err != nil {
		return g, nil
	}
	g.fields = fields
	hooks, ok := fields.Get("hooks")
	if !ok {
		return g, nil
	}
	if json.Unmarshal(hooks, &g.hooks) != nil {
		return nil, fmt.Errorf(`"hooks" in a %s group is not an array`, ev)
	}
	g.handlers = make([]handler, len(g.hooks))
	for i, h := range g.hooks {
		json.Unmarshal(h, &g.handlers[i]) // a handler that is not an object is not stele's
	}
	return g, nil
}

// prune rebuilds ev's groups after changes: handlers marked to drop go, and
// a group they leave empty.
func (f *settingsFile) prune(ev string) error {
	var kept []*group
	for _, g := range f.groups[ev] {
		if len(g.drop) == 0 && !g.dirty {
			kept = append(kept, g)
			continue
		}
		var hooks []json.RawMessage
		var handlers []handler
		for i := range g.hooks {
			if !g.drop[i] {
				hooks = append(hooks, g.hooks[i])
				handlers = append(handlers, g.handlers[i])
			}
		}
		if len(hooks) == 0 {
			continue // stele emptied it
		}
		g.fields.Set("hooks", jsonedit.Array(hooks))
		raw, err := g.fields.Marshal()
		if err != nil {
			return err
		}
		g.raw, g.hooks, g.handlers, g.drop, g.dirty = raw, hooks, handlers, map[int]bool{}, false
		kept = append(kept, g)
	}
	f.groups[ev] = kept
	return nil
}

// render writes the file back: every value as written, laid out with two
// spaces, keeping a byte order mark and CRLF line ends.
func (f *settingsFile) render() ([]byte, error) {
	for _, ev := range events {
		gs := f.groups[ev]
		if len(gs) == 0 {
			if f.present[ev] {
				f.hooks.Remove(ev)
			}
			continue
		}
		raws := make([]json.RawMessage, len(gs))
		for i, g := range gs {
			raws[i] = g.raw
		}
		f.hooks.Set(ev, jsonedit.Array(raws))
	}
	if len(f.hooks) == 0 {
		f.top.Remove("hooks")
	} else {
		h, err := f.hooks.Marshal()
		if err != nil {
			return nil, err
		}
		f.top.Set("hooks", h)
	}
	compact, err := f.top.Marshal()
	if err != nil {
		return nil, err
	}
	return f.doc.Render(compact)
}
