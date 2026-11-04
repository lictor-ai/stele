package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

const (
	// FileName is the log, under .stele/.
	FileName = "log.jsonl"
	// Rotated is the one older log kept when FileName passes MaxSize.
	Rotated = "log.1.jsonl"
	// Version is the format version on every line (D50). A new kind or a new
	// optional field keeps it, and readers skip kinds they do not know;
	// removing a field or changing what one means bumps it.
	Version = 1
	// MaxSize is the size past which the log rotates, so the two files stay
	// about 64 MiB together.
	MaxSize = 32 << 20

	// lockWait is how long a line waits for another writer before it is
	// dropped: the hook must not hold up the agent for a log.
	lockWait = 100 * time.Millisecond
	purpose  = "log"
)

// Kind is what a line records.
type Kind string

const (
	// Loaded: a session or subagent started, with the rules it loaded from
	// the rules file.
	Loaded Kind = "loaded"
	// Denied: a write, or a shell command, was denied to show rules.
	Denied Kind = "denied"
	// Checked: a file was checked after a write.
	Checked Kind = "checked"
	// Wrote: an agent wrote a file, with the rules that apply to it (D70).
	Wrote Kind = "wrote"
	// CheckRun: stele check ran.
	CheckRun Kind = "check"
)

// Header starts every line.
type Header struct {
	V       int       `json:"v"`
	Kind    Kind      `json:"kind"`
	Project string    `json:"project"` // stele.lock's project id (D51)
	TS      time.Time `json:"ts"`      // UTC
	Session string    `json:"session"` // "" for a stele check run
	// Agent is the adapter's Name, such as "claude-code"; "" when no agent
	// ran it.
	Agent string `json:"agent"`
	// Subagent is the agent's own id for a subagent; "" for the main agent.
	Subagent string `json:"subagent,omitempty"`
}

// LoadedRecord: the rules an agent loaded at its start.
type LoadedRecord struct {
	Header
	Rules []string `json:"rules"`
}

// DeniedRecord: a write or a shell command denied to show rules.
type DeniedRecord struct {
	Header
	Prompt string   `json:"prompt,omitempty"` // the user turn's id
	Via    string   `json:"via"`              // "write" or "shell"
	Files  []string `json:"files"`            // relative to the root, / separators
	Rules  []string `json:"rules"`            // the ids shown
	More   int      `json:"more"`             // rules held back: they did not fit
}

// CheckedRecord: one check after a write, clean or not.
type CheckedRecord struct {
	Header
	Prompt     string      `json:"prompt,omitempty"`
	File       string      `json:"file"`       // relative to the root, / separators
	Rules      []string    `json:"rules"`      // the ids checked
	Violations []Violation `json:"violations"` // as found, at most 100 per rule
	More       int         `json:"more"`       // violations past that cap
}

// Violation is one broken rule, where.
type Violation struct {
	Rule     string `json:"rule"`
	Line     int    `json:"line"`
	Blocking bool   `json:"blocking"`
}

// WroteRecord: a write that went through, with every rule whose scope
// applies to the file, shown or loaded, checked or not (D70).
type WroteRecord struct {
	Header
	Prompt string   `json:"prompt,omitempty"`
	File   string   `json:"file"`  // relative to the root, / separators
	Rules  []string `json:"rules"` // the ids that apply
}

// CheckRunRecord: one stele check run and its counts. A count is never named
// as a list of another kind is, so one struct can read every kind.
type CheckRunRecord struct {
	Header
	Mode      string         `json:"mode"` // "changed", "base", "all" or "paths"
	Files     int            `json:"file_count"`
	Rules     int            `json:"rule_count"`
	Problems  int            `json:"problems"`
	Errors    int            `json:"errors"`
	Warnings  int            `json:"warnings"`
	Skipped   int            `json:"skipped"`
	Truncated int            `json:"truncated"`
	Exit      int            `json:"exit"`
	Broken    map[string]int `json:"broken"` // rule id → violations found
}

// Record is a line of the log.
type Record interface {
	header() *Header
	kind() Kind
	// fill makes nil lists empty and sorts ids, so the same event always
	// writes the same line.
	fill()
}

func (r *LoadedRecord) header() *Header   { return &r.Header }
func (r *DeniedRecord) header() *Header   { return &r.Header }
func (r *CheckedRecord) header() *Header  { return &r.Header }
func (r *WroteRecord) header() *Header    { return &r.Header }
func (r *CheckRunRecord) header() *Header { return &r.Header }

func (*LoadedRecord) kind() Kind   { return Loaded }
func (*DeniedRecord) kind() Kind   { return Denied }
func (*CheckedRecord) kind() Kind  { return Checked }
func (*WroteRecord) kind() Kind    { return Wrote }
func (*CheckRunRecord) kind() Kind { return CheckRun }

func (r *LoadedRecord) fill() { r.Rules = sorted(r.Rules) }
func (r *DeniedRecord) fill() { r.Files, r.Rules = sorted(r.Files), sorted(r.Rules) }
func (r *CheckedRecord) fill() {
	r.Rules = sorted(r.Rules)
	if r.Violations == nil {
		r.Violations = []Violation{}
	}
}
func (r *WroteRecord) fill() { r.Rules = sorted(r.Rules) }
func (r *CheckRunRecord) fill() {
	if r.Broken == nil {
		r.Broken = map[string]int{}
	}
}

func sorted(ids []string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

// Append adds r to root's log, signed with key. It writes only where
// .stele/ already is, as a real directory, and never creates it: stele
// check in a fresh CI checkout records nothing. A line that cannot be
// written in lockWait is dropped with an error, which callers ignore.
func Append(root string, key sign.Key, r Record) error {
	if len(key) == 0 {
		return errors.New("log: no key to sign the line with")
	}
	dir := filepath.Join(root, index.Dir)
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory", dir)
	}
	h := r.header()
	h.V, h.Kind = Version, r.kind()
	h.TS = h.TS.UTC()
	r.fill()
	line, err := encode(key, r)
	if err != nil {
		return err
	}
	return safefile.Append(filepath.Join(dir, FileName), filepath.Join(dir, Rotated), line, MaxSize, lockWait)
}

// macField is what encode puts before a line's MAC; a line ends with the MAC
// and `"}`.
const (
	macField = `,"mac":"`
	macLen   = 64
	tailLen  = len(macField) + macLen + len(`"}`)
)

// encode returns r as one line: its JSON with the MAC of that JSON as a last
// field. Verifying needs no canonical form: the reader strips the field and
// has the bytes that were signed.
func encode(key sign.Key, r Record) ([]byte, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	line := make([]byte, 0, len(body)+tailLen+1)
	line = append(line, body[:len(body)-1]...)
	line = append(line, macField...)
	line = append(line, key.MAC(purpose, body)...)
	line = append(line, `"}`+"\n"...)
	return line, nil
}

// decode returns the signed JSON of one line, or false when key did not sign
// it.
func decode(key sign.Key, line []byte) ([]byte, bool) {
	n := len(line)
	if n < tailLen+2 || string(line[n-tailLen:n-tailLen+len(macField)]) != macField || string(line[n-2:]) != `"}` {
		return nil, false
	}
	mac := string(line[n-macLen-2 : n-2])
	body := append(append([]byte{}, line[:n-tailLen]...), '}')
	if !key.Verify(purpose, body, mac) {
		return nil, false
	}
	return body, true
}
