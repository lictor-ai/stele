package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrSinks is a "sinks" key in stele.json stele cannot use.
var ErrSinks = errors.New("bad sinks in stele.json")

// PushTimeout is how long a push call has, unless the entry's "timeout"
// says otherwise (D82).
const PushTimeout = time.Minute

// sinkEntry is one item of "sinks" in stele.json.
type sinkEntry struct {
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Timeout string          `json:"timeout"`
	// ShareBriefs is reserved: no record holds a brief, so it has no effect
	// yet (D82).
	ShareBriefs bool `json:"share_briefs"`
}

// Sink is an external sink: a stele-sink-<name> on PATH (D82).
type Sink struct {
	Name        string
	ShareBriefs bool

	bin     string
	config  json.RawMessage
	timeout time.Duration
	run     Runner
	missing error // not found or refused on PATH

	once sync.Once
	derr error
}

// Missing is why the sink cannot run: not found on PATH, or refused. nil
// for a sink that was found.
func (s *Sink) Missing() error { return s.missing }

// Sinks is every sink stele.json lists under root, in its order, each looked
// up on PATH with run. A sink not found or refused is still listed, with its
// Missing error, which its Push returns: the others can still be pushed to.
// A bad entry fails the list. There is no built-in sink. Without a runner it
// fails.
func Sinks(root string, run Runner) ([]*Sink, error) {
	msgs, err := listedRaw(root, "sinks", ErrSinks)
	if err != nil {
		return nil, err
	}
	var out []*Sink
	seen := map[string]int{}
	for i, m := range msgs {
		var e sinkEntry
		if err := strict(m, &e); err != nil {
			return nil, fmt.Errorf("%w: sinks[%d]: %v", ErrSinks, i, err)
		}
		if j, ok := seen[e.Name]; ok {
			return nil, fmt.Errorf("%w: sinks[%d]: %q is listed twice, as sinks[%d]", ErrSinks, i, e.Name, j)
		}
		seen[e.Name] = i
		p, err := external(KindSink, "sinks", entry{Name: e.Name, Config: e.Config, Timeout: e.Timeout}, PushTimeout, run)
		if err != nil {
			var pe *Error
			if errors.As(err, &pe) {
				// Not found or refused: this sink fails, the others still
				// get their lines (D83).
				out = append(out, &Sink{Name: e.Name, ShareBriefs: e.ShareBriefs, missing: err})
				continue
			}
			return nil, fmt.Errorf("%w: sinks[%d]: %v", ErrSinks, i, err)
		}
		out = append(out, &Sink{Name: p.name, ShareBriefs: e.ShareBriefs, bin: p.bin, config: p.config, timeout: p.timeout, run: run})
	}
	return out, nil
}

// SinkNames is the names of the sinks stele.json lists under root, found or
// not; nothing is looked up. A bad stele.json is an error.
func SinkNames(root string) ([]string, error) {
	msgs, err := listedRaw(root, "sinks", ErrSinks)
	if err != nil {
		return nil, err
	}
	var out []string
	for i, m := range msgs {
		var e sinkEntry
		if err := strict(m, &e); err != nil {
			return nil, fmt.Errorf("%w: sinks[%d]: %v", ErrSinks, i, err)
		}
		if err := ValidName(e.Name); err != nil {
			return nil, fmt.Errorf("%w: sinks[%d]: %v", ErrSinks, i, err)
		}
		out = append(out, e.Name)
	}
	return out, nil
}

func (s *Sink) fail(op string, err error) error {
	return &Error{Kind: KindSink, Name: s.Name, List: "sinks", Exe: Exe(KindSink, s.Name), Op: op, Err: err}
}

// Push hands records to the sink, for project. accepted is how many it
// took: the first accepted of them, in order (D82).
func (s *Sink) Push(ctx context.Context, project string, records []json.RawMessage) (accepted int, err error) {
	if s.missing != nil {
		return 0, s.missing
	}
	s.once.Do(func() { _, s.derr = describe(ctx, s.run, KindSink, s.Name, s.bin) })
	if s.derr != nil {
		return 0, s.derr
	}
	if records == nil {
		records = []json.RawMessage{}
	}
	req, err := Request("push", map[string]any{"config": s.config, "project": project, "records": records})
	if err != nil {
		return 0, s.fail("push", err)
	}
	out, err := s.run.Run(ctx, s.bin, req, SmallAnswer, s.timeout)
	if err != nil {
		return 0, s.fail("push", err)
	}
	if !utf8.Valid(out) {
		return 0, s.fail("push", fmt.Errorf("%w: answer is not valid UTF-8", ErrProtocol))
	}
	var a struct {
		Accepted *int `json:"accepted"`
	}
	if err := DecodeAnswer(out, &a); err != nil {
		return 0, s.fail("push", err)
	}
	if a.Accepted == nil {
		return 0, s.fail("push", fmt.Errorf("%w: no \"accepted\"", ErrProtocol))
	}
	if *a.Accepted < 0 || *a.Accepted > len(records) {
		return 0, s.fail("push", fmt.Errorf("%w: accepted %d of %d records", ErrProtocol, *a.Accepted, len(records)))
	}
	return *a.Accepted, nil
}
