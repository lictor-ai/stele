package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/printable"
)

// Protocol is the version of the plugin protocol this stele speaks (D78).
const Protocol = 1

// Kind is what a plugin is: the middle word of its executable's name.
type Kind string

const (
	KindSource  Kind = "source"
	KindChecker Kind = "checker"
	KindSink    Kind = "sink"
)

// Exe is the executable stele looks for on PATH for the plugin name of kind.
func Exe(kind Kind, name string) string { return "stele-" + string(kind) + "-" + name }

var (
	// ErrPluginNotFound is a listed plugin not on PATH.
	ErrPluginNotFound = errors.New("not found on PATH")
	// ErrPluginRefused is a plugin stele will not run: one inside the project,
	// found through a relative PATH entry, or not a program stele can start
	// without a shell.
	ErrPluginRefused = errors.New("refused")
	// ErrProtocol is an answer stele cannot use: not one JSON object, another
	// protocol, a missing field, or more than stele reads.
	ErrProtocol = errors.New("plugin protocol error")
	// ErrTimeout is a plugin that did not answer in time. It was stopped.
	ErrTimeout = errors.New("no answer")
	// ErrAnswer is a plugin that answered with an error of its own.
	ErrAnswer = errors.New("the plugin reported an error")
	// ErrCancelled is a call stopped because stele was: Ctrl-C, or a
	// signal to stop.
	ErrCancelled = errors.New("cancelled")
)

// Runner finds and starts plugin executables. internal/pluginexec is the only
// one, and only cmd/stele/sources.go hands it over: the hook never has one,
// so no plugin can run on its path (D45, D79).
type Runner interface {
	// Find returns the absolute path of the executable for the plugin name
	// of kind, found on PATH and outside the project (D43), or an error that
	// is ErrPluginNotFound or ErrPluginRefused.
	Find(kind Kind, name string) (bin string, err error)
	// Run starts bin, writes req to its stdin, and returns what it wrote to
	// stdout: at most max bytes, in at most timeout.
	Run(ctx context.Context, bin string, req []byte, max int, timeout time.Duration) ([]byte, error)
}

// name is a plugin name: lower-case letters, digits and dashes, starting with
// a letter, so it can never be a path.
var name = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// reserved are the built-in plugins' names, for every kind: a listed plugin
// with one would be ambiguous in stele.lock and in messages.
var reserved = map[string]bool{"stelemd": true, "git": true, "regex": true}

// ValidName reports why n cannot name an external plugin, or nil.
func ValidName(n string) error {
	if reserved[n] {
		return fmt.Errorf("%q is built in: remove it from %s", n, enforce.FileName)
	}
	if !name.MatchString(n) {
		return fmt.Errorf("%q: a plugin name is lower-case letters, digits and dashes, starting with a letter, at most 64", printable.Clean(n))
	}
	return nil
}

// Request is the JSON request op, with fields, and "protocol": 1.
func Request(op string, fields map[string]any) ([]byte, error) {
	m := map[string]any{"protocol": Protocol, "op": op}
	for k, v := range fields {
		m[k] = v
	}
	return json.Marshal(m)
}

// DecodeAnswer decodes a plugin's answer into v. The answer must be one JSON
// object, nothing after it but space, with "protocol": 1. One with an "error"
// is ErrAnswer, holding the plugin's message, cleaned. Fields v does not have
// are ignored, so a later plugin can add some.
func DecodeAnswer(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%w: no answer on stdout", ErrProtocol)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return fmt.Errorf("%w: answer is not valid JSON (starts %q)", ErrProtocol, start(data))
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: more after the answer", ErrProtocol)
	}
	if err := envelopeKeys(raw); err != nil {
		return err
	}
	var env struct {
		Protocol *int   `json:"protocol"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	switch {
	case env.Protocol == nil:
		return fmt.Errorf("%w: answer has no protocol", ErrProtocol)
	case *env.Protocol != Protocol:
		return fmt.Errorf("%w: answer speaks protocol %d; this stele speaks %d", ErrProtocol, *env.Protocol, Protocol)
	case env.Error != "":
		return &answerError{msg: cut(printable.Clean(env.Error), 1024)}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return nil
}

// envelopeKeys refuses an answer whose top-level "protocol" or "error" is
// spelled in another case, or given twice: Go's decoder would take any case,
// and the last of two, so the answer could say one thing to stele and
// another to whatever else reads it.
func envelopeKeys(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		key, _ := t.(string)
		for _, k := range []string{"protocol", "error"} {
			if strings.EqualFold(key, k) {
				if key != k || seen[k] {
					return fmt.Errorf("%w: %q must be given once, in lower case", ErrProtocol, k)
				}
				seen[k] = true
			}
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return fmt.Errorf("%w: %v", ErrProtocol, err)
		}
	}
	return nil
}

// answerError is the error a plugin answered with. It is ErrAnswer.
type answerError struct{ msg string }

func (e *answerError) Error() string        { return e.msg }
func (e *answerError) Is(target error) bool { return target == ErrAnswer }

// start is the first bytes of data, cleaned, for a message.
func start(data []byte) string {
	return cut(printable.Clean(string(data)), 40)
}

// cut keeps at most n bytes of s, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Timeout is ErrTimeout after d.
func Timeout(d time.Duration) error { return fmt.Errorf("%w in %v, stopped", ErrTimeout, d) }

// ExitError is a plugin that exited with a non-zero status.
type ExitError struct {
	Code   int
	Stderr string // the end of what it wrote on stderr, cleaned
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("exited with status %d", e.Code)
	tail := lastLines(e.Stderr, 5)
	if tail == "" {
		return msg
	}
	return msg + ": " + cut(printable.Clean(tail), 1024)
}

// lastLines is the last n non-empty lines of s, joined by spaces.
func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " ")
}

// Error is what went wrong with one plugin: finding it (Op "") or one call.
// It names the executable, so the user knows what to install or fix.
type Error struct {
	Kind Kind
	Name string
	List string // the stele.json key that lists it: "sources" or "checkers"
	Exe  string // the executable, as found, or its name
	Op   string // the call, or "" when it was not found or refused
	Err  error
}

func (e *Error) Error() string {
	if e.Op == "" {
		if errors.Is(e.Err, ErrPluginNotFound) {
			msg := fmt.Sprintf("%s %q: %s not found on PATH", e.Kind, e.Name, Exe(e.Kind, e.Name))
			if e.List != "" {
				msg += fmt.Sprintf(": install it, or remove %q from %q in %s", e.Name, e.List, enforce.FileName)
			}
			return msg
		}
		return fmt.Sprintf("%s %q: %v", e.Kind, e.Name, e.Err)
	}
	msg := fmt.Sprintf("%s %s: %v", e.Exe, e.Op, e.Err)
	if errors.Is(e.Err, ErrTimeout) && e.Name != "" {
		msg += fmt.Sprintf(": set a longer \"timeout\" for %q in %s", e.Name, enforce.FileName)
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Info is what a plugin's describe says of it.
type Info struct {
	Exe       string `json:"-"` // where it was found
	Kind      Kind   `json:"kind"`
	Protocols []int  `json:"protocols"`
	Version   string `json:"version"`
}

// describe timeouts and caps (D78). The other calls have their own.
const (
	DescribeTimeout = 10 * time.Second
	SmallAnswer     = 1 << 20  // describe, fingerprint, validate
	LargeAnswer     = 32 << 20 // read, check
)

// Describe finds the plugin name of kind and asks it what it is.
func Describe(ctx context.Context, run Runner, kind Kind, n string) (Info, error) {
	bin, err := run.Find(kind, n)
	if err != nil {
		return Info{}, &Error{Kind: kind, Name: n, Err: err}
	}
	return describe(ctx, run, kind, n, bin)
}

// describe asks the plugin at bin what it is, and checks it is a plugin of
// kind that speaks Protocol.
func describe(ctx context.Context, run Runner, kind Kind, n, bin string) (Info, error) {
	fail := func(err error) (Info, error) {
		return Info{}, &Error{Kind: kind, Name: n, Exe: Exe(kind, n), Op: "describe", Err: err}
	}
	req, err := Request("describe", nil)
	if err != nil {
		return fail(err)
	}
	out, err := run.Run(ctx, bin, req, SmallAnswer, DescribeTimeout)
	if err != nil {
		return fail(err)
	}
	var info Info
	if err := DecodeAnswer(out, &info); err != nil {
		return fail(err)
	}
	info.Exe = bin
	if info.Version == "" || len(info.Version) > 64 || printable.Clean(info.Version) != info.Version {
		return fail(fmt.Errorf("%w: version is 1 to 64 printable characters", ErrProtocol))
	}
	if info.Kind != kind {
		return fail(fmt.Errorf("%w: describe says it is a %s, not a %s", ErrProtocol, printable.Clean(string(info.Kind)), kind))
	}
	speaks := false
	for _, p := range info.Protocols {
		speaks = speaks || p == Protocol
	}
	if !speaks {
		return Info{}, &Error{Kind: kind, Name: n, Exe: Exe(kind, n), Op: "describe",
			Err: fmt.Errorf("%w: %s %s speaks protocol %v; this stele speaks %d: upgrade stele, or install a version of the plugin that speaks %d",
				ErrProtocol, Exe(kind, n), info.Version, info.Protocols, Protocol, Protocol)}
	}
	return info, nil
}
