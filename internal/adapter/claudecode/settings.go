package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
)

// SettingsFile is where init registers the hooks, relative to the root: the
// project settings Claude Code reads from the directory it starts in, and
// that the team commits next to stele.md.
var SettingsFile = filepath.Join(".claude", "settings.json")

// registration is one hook init registers: an event, and the matcher of its
// group, "" for an event that takes none.
type registration struct{ Event, Matcher string }

// registrations are the hooks init registers: before a write to show the
// rules, after it to check them (ADR-0004), and at the start of a session or
// subagent to tell the agent to write files with Write and Edit only (#50).
// One group per event: Install counts exactly one stele handler per event.
var registrations = []registration{
	{"PreToolUse", preTools},
	{"PostToolUse", writeTools},
	{"SessionStart", ""},
	{"SubagentStart", ""},
}

// writeTools is the tools the write hooks handle, an exact list.
const writeTools = "Write|Edit|MultiEdit"

// preTools adds Bash before a tool runs only: stele reads the command for
// the files it writes (#57). Nothing runs after a Bash command.
const preTools = writeTools + "|Bash"

// events are the events of registrations, in order.
var events = func() []string {
	var es []string
	for _, r := range registrations {
		es = append(es, r.Event)
	}
	return es
}()

// Events returns the events init registers stele hook on, in order.
func Events() []string { return append([]string(nil), events...) }

// installedFlag marks stele's own handlers and stamps them with the version
// that wrote them. Settings files are JSON, which has no comments, and
// Claude Code validates them against a schema, so the marker lives in the
// command itself: it survives a fresh clone and costs the hook nothing to
// read.
const installedFlag = "--installed="

// handler is one hook handler, as much of it as stele reads.
type handler struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// words is the handler's command line, split on spaces outside quotes.
func (h handler) words() []string {
	return append(split(h.Command), h.Args...)
}

// split splits a command line on spaces, keeping what is inside single or
// double quotes together, quotes removed. A backslash is a character, as in
// a Windows path.
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
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	return name == "stele"
}

// Switches are the parts of the write hooks a project turned off.
type Switches = adapter.Switches

// The switch flags. A switch that is on is never written.
const (
	remindOff = "--remind=off"
	checkOff  = "--check=off"
)

// flags are the words init writes after the stamp, in this order.
func flags(s Switches) []string {
	var f []string
	if s.RemindOff {
		f = append(f, remindOff)
	}
	if s.CheckOff {
		f = append(f, checkOff)
	}
	return f
}

// stamp returns the version and switches on a handler init wrote: exactly
// stele hook --installed=<version>, then the flags of the switches that are
// off, in init's order, nothing else. ok is false for any other handler,
// including a stele hook written by hand or one the user added to.
func (h handler) stamp() (version string, sw Switches, ok bool) {
	w := h.words()
	if !h.runsStele() || len(w) < 3 || !strings.HasPrefix(w[2], installedFlag) {
		return "", Switches{}, false
	}
	sw = HookSwitches(w[3:])
	if !slices.Equal(w[3:], flags(sw)) {
		return "", Switches{}, false
	}
	return strings.TrimPrefix(w[2], installedFlag), sw, true
}

// command is the command init writes for version and switches.
func command(version string, sw Switches) string {
	return strings.Join(append([]string{"stele", "hook", installedFlag + version}, flags(sw)...), " ")
}

// Install returns settings with stele's hooks on every event of
// registrations, stamped with version and carrying sw. Empty settings are
// {}. changed is false, and out is settings unchanged, when each event
// already has exactly one stele handler, carrying this version and these
// switches, in a group with the registered matcher. Otherwise every stele
// handler is removed and one group per event appended. notes name stele
// hooks written by hand, which are left alone. Malformed settings are an
// error, never rewritten.
func Install(settings []byte, version string, sw Switches) (out []byte, changed bool, notes []string, err error) {
	f, err := parseSettings(settings)
	if err != nil {
		return nil, false, nil, err
	}
	for _, r := range registrations {
		var current []bool
		for _, g := range f.groups[r.Event] {
			for _, h := range g.handlers {
				if v, s, ok := h.stamp(); ok {
					// In a group init did not write: out of date.
					current = append(current, g.matcher() == r.Matcher && v == version && s == sw)
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
	f.removeStele()
	for _, r := range registrations {
		fresh, err := marshal(struct {
			Matcher string `json:"matcher,omitempty"`
			Hooks   []any  `json:"hooks"`
		}{r.Matcher, []any{struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		}{"command", command(version, sw), 10}}})
		if err != nil {
			return nil, false, nil, err
		}
		f.groups[r.Event] = append(f.groups[r.Event], &group{raw: fresh})
	}
	out, err = f.render()
	return out, err == nil, notes, err
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
	removed = f.removeStele()
	if removed == 0 {
		return settings, 0, false, nil
	}
	out, err = f.render()
	if err != nil {
		return nil, 0, false, err
	}
	return out, removed, len(f.top) == 0, nil
}

// Installed returns the version and switches on stele's hooks.
func Installed(settings []byte) (version string, sw Switches, ok bool) {
	f, err := parseSettings(settings)
	if err != nil {
		return "", Switches{}, false
	}
	for _, ev := range events {
		for _, g := range f.groups[ev] {
			for _, h := range g.handlers {
				if v, s, ok := h.stamp(); ok {
					return v, s, true
				}
			}
		}
	}
	return "", Switches{}, false
}

// OtherSteleHook reports whether settings run stele hook, on any event, other
// than as init writes it for version and sw. Claude Code runs identical
// handlers once, so only another one makes stele run twice. Unreadable
// settings run nothing stele can see.
func OtherSteleHook(settings []byte, version string, sw Switches) bool {
	var s struct {
		Hooks map[string][]struct {
			Hooks []handler `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(settings, &s) != nil {
		return false
	}
	for _, groups := range s.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if h.runsStele() && (h.Command != command(version, sw) || len(h.Args) > 0) {
					return true
				}
			}
		}
	}
	return false
}

// InstalledVersion reads the stamp from stele hook's own arguments: the
// value of --installed=, or "" when there is none.
func InstalledVersion(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, installedFlag); ok {
			return v
		}
	}
	return ""
}

// HookSwitches reads the switches from stele hook's own arguments. The last
// of --remind=on|off wins, likewise --check; anything else is ignored, so a
// newer stele's flags leave an older one with both on.
func HookSwitches(args []string) Switches {
	var sw Switches
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

// member is one key of a JSON object, its key and value kept exactly as
// written. Raw is empty for a key stele adds.
type member struct {
	Key   string
	Value json.RawMessage
	Raw   []byte
}

// object is a JSON object in its written order. stele edits only "hooks" and
// two of its events; every other value is written back byte for byte, so
// number literals, escapes and key order survive.
type object []member

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// set replaces key's value, or appends key when it is missing.
func (o *object) set(key string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Value = v
			return
		}
	}
	*o = append(*o, member{Key: key, Value: v})
}

func (o *object) remove(key string) {
	for i := range *o {
		if (*o)[i].Key == key {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

func (o object) marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k := m.Raw
		if k == nil {
			var err error
			if k, err = marshal(m.Key); err != nil {
				return nil, err
			}
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal encodes v without escaping <, > and &, as Claude Code writes them.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// array joins values already encoded into a JSON array. json.Marshal would
// re-escape them.
func array(vs []json.RawMessage) json.RawMessage {
	b := []byte{'['}
	for i, v := range vs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, v...)
	}
	return append(b, ']')
}

// parseObject reads a JSON object. what names it in errors. A key that
// appears twice is an error: which one Claude Code keeps is not stele's to
// guess.
func parseObject(data []byte, what string) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	var o object
	seen := map[string]bool{}
	for dec.More() {
		start := dec.InputOffset()
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := t.(string) // an object's tokens alternate key, value
		// The key's bytes as written, escapes and all, without the comma
		// and space before it.
		raw := bytes.TrimLeft(data[start:dec.InputOffset()], ", \t\r\n")
		if seen[key] {
			return nil, fmt.Errorf("%q appears twice in %s", key, what)
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, member{Key: key, Value: v, Raw: raw})
	}
	return o, nil
}

// group is one matcher group of an event, decoded only as far as finding
// stele's handlers in it.
type group struct {
	raw      json.RawMessage // as written, or rebuilt after a removal
	fields   object
	hooks    []json.RawMessage
	handlers []handler // hooks, decoded
}

// settingsFile is a parsed settings file.
type settingsFile struct {
	bom, crlf bool
	top       object
	hooks     object
	groups    map[string][]*group // the events stele edits
	present   map[string]bool     // events the file has
}

var bom = []byte("\xef\xbb\xbf")

func parseSettings(data []byte) (*settingsFile, error) {
	f := &settingsFile{groups: map[string][]*group{}, present: map[string]bool{}}
	body, hasBOM := bytes.CutPrefix(data, bom)
	f.bom = hasBOM
	f.crlf = bytes.Contains(body, []byte("\r\n"))
	if len(bytes.TrimSpace(body)) == 0 {
		return f, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line, col := position(body, se.Offset)
			return nil, fmt.Errorf("invalid JSON at line %d, column %d: %v", line, col, err)
		}
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	top, err := parseObject(body, "the settings file")
	if err != nil {
		return nil, err
	}
	f.top = top
	raw, ok := top.get("hooks")
	if !ok {
		return f, nil
	}
	if f.hooks, err = parseObject(raw, `"hooks"`); err != nil {
		return nil, err
	}
	for _, ev := range events {
		raw, ok := f.hooks.get(ev)
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

// matcher returns the group's matcher, "" when it has none or it is not a
// string.
func (g *group) matcher() string {
	raw, ok := g.fields.get("matcher")
	if !ok {
		return ""
	}
	var m string
	json.Unmarshal(raw, &m)
	return m
}

// parseGroup reads one matcher group. A group that is not an object is left
// as it is: it holds no handler stele could have written.
func parseGroup(raw json.RawMessage, ev string) (*group, error) {
	g := &group{raw: raw}
	fields, err := parseObject(raw, "a "+ev+" group")
	if err != nil {
		return g, nil
	}
	g.fields = fields
	hooks, ok := fields.get("hooks")
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

// removeStele takes out every handler Install wrote, and the groups, events
// and "hooks" that leaves empty. It returns how many it removed.
func (f *settingsFile) removeStele() int {
	removed := 0
	for _, ev := range events {
		var kept []*group
		for _, g := range f.groups[ev] {
			var hooks []json.RawMessage
			var handlers []handler
			for i, h := range g.handlers {
				if _, _, ok := h.stamp(); ok {
					removed++
					continue
				}
				hooks = append(hooks, g.hooks[i])
				handlers = append(handlers, h)
			}
			if len(handlers) == len(g.handlers) {
				kept = append(kept, g)
				continue
			}
			if len(hooks) == 0 {
				continue // stele emptied it
			}
			g.fields.set("hooks", array(hooks))
			g.raw, _ = g.fields.marshal()
			g.hooks, g.handlers = hooks, handlers
			kept = append(kept, g)
		}
		f.groups[ev] = kept
	}
	return removed
}

// render writes the file back: every value as written, laid out with two
// spaces as Claude Code does, keeping a byte order mark and CRLF line ends.
func (f *settingsFile) render() ([]byte, error) {
	for _, ev := range events {
		gs := f.groups[ev]
		if len(gs) == 0 {
			if f.present[ev] {
				f.hooks.remove(ev)
			}
			continue
		}
		raws := make([]json.RawMessage, len(gs))
		for i, g := range gs {
			raws[i] = g.raw
		}
		f.hooks.set(ev, array(raws))
	}
	if len(f.hooks) == 0 {
		f.top.remove("hooks")
	} else {
		h, err := f.hooks.marshal()
		if err != nil {
			return nil, err
		}
		f.top.set("hooks", h)
	}
	compact, err := f.top.marshal()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := json.Indent(&b, compact, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	out := b.Bytes()
	if f.crlf {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	if f.bom {
		out = append(append([]byte{}, bom...), out...)
	}
	return out, nil
}

// position turns a byte offset into a 1-based line and column.
func position(data []byte, offset int64) (line, col int) {
	before := data[:min(int(offset), len(data))]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}
