package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// ErrSources is a "sources" key in stele.json stele cannot use.
var ErrSources = errors.New("bad sources in " + enforce.FileName)

// maxGitSources caps the git sources stele.json may list.
const maxGitSources = 16

// Sources is every source stele compile reads under root: the builtins,
// then each git source stele.json lists, in its order (D71). git is what
// the git sources fetch with; nothing is fetched here. It is never what the
// hook reads: that is Builtins (D45).
func Sources(root string, git gitrepo.Git, mode gitrepo.Mode) ([]source.Source, error) {
	gits, err := GitSources(root, git, mode)
	if err != nil {
		return nil, err
	}
	srcs := Builtins()
	for _, g := range gits {
		srcs = append(srcs, g)
	}
	return srcs, nil
}

// GitSources is the git sources stele.json lists under "sources". No
// stele.json, or no "sources", is none.
func GitSources(root string, git gitrepo.Git, mode gitrepo.Mode) ([]*gitrepo.Source, error) {
	data, err := enforce.Read(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Sources []json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSources, err)
	}
	if len(raw.Sources) > maxGitSources {
		return nil, fmt.Errorf("%w: %d sources, at most %d", ErrSources, len(raw.Sources), maxGitSources)
	}
	var out []*gitrepo.Source
	seen := map[string]int{}
	for i, msg := range raw.Sources {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("%w: sources[%d]: %s", ErrSources, i, fmt.Sprintf(format, a...))
		}
		var entry struct {
			Name   string          `json:"name"`
			Config json.RawMessage `json:"config"`
		}
		if err := strict(msg, &entry); err != nil {
			return nil, bad("%v", err)
		}
		if entry.Name != gitrepo.Name {
			return nil, bad("%q: the one source stele.json can list is %q; external sources are not supported yet", entry.Name, gitrepo.Name)
		}
		var cfg gitrepo.Config
		if len(entry.Config) == 0 {
			return nil, bad(`no "config"; for example {"name": "git", "config": {"repo": "git@github.com:me/guidelines.git"}}`)
		}
		if err := strict(entry.Config, &cfg); err != nil {
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
		out = append(out, s)
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
