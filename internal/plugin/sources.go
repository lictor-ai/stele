package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// ErrSources is a "sources" key in stele.json stele cannot use.
var ErrSources = errors.New("bad sources in " + enforce.FileName)

// maxEntries caps the sources, and the checkers, stele.json may list.
const maxEntries = 16

// The time limits of one call, and the bounds of an entry's "timeout" (D80).
const (
	ReadTimeout  = time.Minute
	CheckTimeout = 5 * time.Minute
	minTimeout   = time.Second
	maxTimeout   = 30 * time.Minute
)

// entry is one item of "sources" or "checkers" in stele.json.
type entry struct {
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Timeout string          `json:"timeout"`
}

// Sources is every source stele compile reads under root: the builtins, then
// each source stele.json lists, in its order: a git source (D71) for "git",
// an external one (D78) for any other name. git is what the git sources fetch
// with, and run what starts the external ones; nothing is fetched or started
// here, but each external source is found on PATH, so a missing one fails
// before any rule is read. It is never what the hook reads: that is Builtins
// (D45, D79).
func Sources(root string, git gitrepo.Git, mode gitrepo.Mode, run Runner) ([]source.Source, error) {
	entries, err := listed(root, "sources", ErrSources)
	if err != nil {
		return nil, err
	}
	srcs := Builtins()
	seen := map[string]int{}
	for i, e := range entries {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("%w: sources[%d]: %s", ErrSources, i, fmt.Sprintf(format, a...))
		}
		if e.Name == gitrepo.Name {
			if e.Timeout != "" {
				return nil, bad(`"timeout" is for external plugins; git has its own limits`)
			}
			var cfg gitrepo.Config
			if len(e.Config) == 0 {
				return nil, bad(`no "config"; for example {"name": "git", "config": {"repo": "git@github.com:me/guidelines.git"}}`)
			}
			if err := strict(e.Config, &cfg); err != nil {
				return nil, bad("config: %v", err)
			}
			s, err := gitrepo.New(cfg, git, mode)
			if err != nil {
				return nil, bad("%v", err)
			}
			if j, ok := seen[s.Name()]; ok {
				return nil, bad("reads %s, as sources[%d] does", s.Label(), j)
			}
			seen[s.Name()] = i
			srcs = append(srcs, s)
			continue
		}
		s, err := external(KindSource, "sources", e, ReadTimeout, run)
		if err != nil {
			var pe *Error
			if errors.As(err, &pe) {
				return nil, err
			}
			return nil, bad("%v", err)
		}
		if j, ok := seen[e.Name]; ok {
			return nil, bad("%q is listed twice, as sources[%d]", e.Name, j)
		}
		seen[e.Name] = i
		srcs = append(srcs, &execSource{name: s.name, bin: s.bin, config: s.config, timeout: s.timeout, run: run})
	}
	return srcs, nil
}

// plugged is what an external entry resolves to.
type plugged struct {
	name, bin string
	config    json.RawMessage
	timeout   time.Duration
}

// external checks e, an entry naming an external plugin of kind, and finds
// it on PATH with run. def is the call's time limit when e sets none.
func external(kind Kind, list string, e entry, def time.Duration, run Runner) (plugged, error) {
	if err := ValidName(e.Name); err != nil {
		return plugged{}, err
	}
	timeout, err := timeoutOf(e.Timeout, def)
	if err != nil {
		return plugged{}, err
	}
	if run == nil {
		return plugged{}, fmt.Errorf("%s %q: external plugins run only from stele compile, stele check and stele init", kind, e.Name)
	}
	bin, err := run.Find(kind, e.Name)
	if err != nil {
		return plugged{}, &Error{Kind: kind, Name: e.Name, List: list, Err: err}
	}
	config := e.Config
	if len(bytes.TrimSpace(config)) == 0 || string(bytes.TrimSpace(config)) == "null" {
		config = json.RawMessage(`{}`)
	}
	return plugged{name: e.Name, bin: bin, config: config, timeout: timeout}, nil
}

// timeoutOf is an entry's "timeout", or def when it sets none.
func timeoutOf(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < minTimeout || d > maxTimeout {
		return 0, fmt.Errorf(`"timeout": %q is not a duration from %v to %v, such as "2m"`, s, minTimeout, maxTimeout)
	}
	return d, nil
}

// listed decodes the entries under key in root's stele.json. No stele.json,
// or no key, is none.
func listed(root, key string, kind error) ([]entry, error) {
	data, err := enforce.Read(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", kind, err)
	}
	var msgs []json.RawMessage
	if v, ok := raw[key]; ok {
		if err := json.Unmarshal(v, &msgs); err != nil {
			return nil, fmt.Errorf("%w: %q: %v", kind, key, err)
		}
	}
	if len(msgs) > maxEntries {
		return nil, fmt.Errorf("%w: %d %s, at most %d", kind, len(msgs), key, maxEntries)
	}
	out := make([]entry, len(msgs))
	for i, m := range msgs {
		if err := strict(m, &out[i]); err != nil {
			return nil, fmt.Errorf("%w: %s[%d]: %v", kind, key, i, err)
		}
	}
	return out, nil
}

// strict decodes data into v, refusing a key v does not have: a typo such as
// "branch" for "ref" must not be ignored.
func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
