package status

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/compile"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/install"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// Version is the format version of stele status --format json (D50).
const Version = 1

// Input is what Collect describes, handed in by cmd/stele.
type Input struct {
	Version string            // this stele's
	Agents  []adapter.Adapter // every agent stele knows
	Items   []plugin.Item     // plugin.Inventory
	// Run starts plugins: describe for each listed one, fingerprint for
	// each external source. nil is --offline: nothing starts.
	Run plugin.Runner
	Key sign.Key // this machine's key; nil when there is none
	// KeyError is why the key could not be read, when there is one but it
	// cannot be used. Key is then nil.
	KeyError error
	// Sinks are the sinks stele.json lists, with the lines waiting for each.
	Sinks []Sink
}

// Status describes one install (D85).
type Status struct {
	V       int      `json:"v"`
	Version string   `json:"stele_version"`
	Project string   `json:"project"`
	Agents  []Agent  `json:"agents"`
	Plugins []Plugin `json:"plugins"`
	Sources []Source `json:"sources"`
	Index   Index    `json:"index"`
	Sinks   []Sink   `json:"sinks"`
	Notes   []string `json:"notes"`
}

// Agent is what init installed for one agent.
type Agent struct {
	Name         string `json:"name"`
	SettingsFile string `json:"settings_file"` // relative to the root, / separators
	Hooks        bool   `json:"hooks"`         // stele's hooks are installed
	Installed    string `json:"installed_version"`
	Remind       bool   `json:"remind"`
	Check        bool   `json:"check"`
	RulesFile    string `json:"rules_file"`
	RulesPresent bool   `json:"rules_file_present"` // it holds stele's rules
	// MCP, Subagent and Skills are what a later stele may install; v1
	// installs none of them (ADR-0004).
	MCP      bool `json:"mcp"`
	Subagent bool `json:"subagent"`
	Skills   bool `json:"skills"`
}

// Plugin is one plugin, built in or listed.
type Plugin struct {
	Kind    plugin.Kind `json:"kind"`
	Name    string      `json:"name"`
	Builtin bool        `json:"builtin"`
	Found   *bool       `json:"found"`   // nil: not looked up (--offline)
	Version string      `json:"version"` // from describe; stele's for a built-in
	Error   string      `json:"error,omitempty"`
}

// Source is one source and whether the index still has its rules as they
// are.
type Source struct {
	Name    string `json:"name"`
	Rules   int    `json:"rules"`   // in the index
	Indexed string `json:"indexed"` // the fingerprint the index holds
	Fresh   *bool  `json:"fresh"`   // nil: not known (--offline, or it failed)
}

// Index is the compiled index.
type Index struct {
	Present bool  `json:"present"`
	Valid   bool  `json:"valid"` // signed by this machine's key
	Fresh   *bool `json:"fresh"` // nil: not known
}

// Sink is one sink and the log lines waiting for it.
type Sink struct {
	Name     string `json:"name"`
	Pending  int    `json:"pending"`
	LastPush string `json:"last_push,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Collect describes the install under root. It writes nothing; with in.Run
// it starts each listed plugin's describe and each external source's
// fingerprint. A plugin that is missing or fails is reported, not an error.
func Collect(ctx context.Context, root string, in Input) (Status, error) {
	st := Status{V: Version, Version: in.Version, Agents: []Agent{}, Plugins: []Plugin{},
		Sources: []Source{}, Sinks: append([]Sink{}, in.Sinks...), Notes: []string{}}
	note := func(s string) { st.Notes = append(st.Notes, s) }

	if f, err := lock.Load(root); err == nil {
		st.Project = f.Project
	} else {
		note("stele.lock: " + err.Error())
	}
	if st.Project == "" {
		note("no project id yet: run stele compile")
	}

	for _, a := range in.Agents {
		st.Agents = append(st.Agents, agentOf(root, a))
	}

	for _, it := range in.Items {
		st.Plugins = append(st.Plugins, pluginOf(ctx, it, in))
	}

	idx, present, valid := loadIndex(root, in.Key)
	st.Index.Present, st.Index.Valid = present, valid
	if in.KeyError != nil {
		note("this machine's key cannot be read: " + in.KeyError.Error())
	}
	switch {
	case !present:
		note("no index: run stele compile")
	case in.Key == nil && in.KeyError != nil:
	case in.Key == nil:
		note("no key on this machine: the index cannot be checked")
	case !valid:
		note("the index is not signed by this machine's key: run stele compile")
	}
	counts := map[string]int{}
	for _, e := range idx.Rules {
		counts[e.Source]++
	}
	known, fresh := valid, true
	seen := map[string]bool{}
	badGit := false
	for _, it := range in.Items {
		if it.Kind != plugin.KindSource {
			continue
		}
		if it.Err != nil && it.Name == gitrepo.Name {
			// A git entry stele cannot read has no source name: the index's
			// git sources may be its rules.
			badGit = true
			continue
		}
		s := Source{Name: it.Name, Rules: counts[it.Name], Indexed: idx.Sources[it.Name]}
		if it.Err != nil {
			note(it.Name + ": " + it.Err.Error())
		} else if it.Source != nil {
			s.Name = it.Source.Name()
			s.Rules, s.Indexed = counts[s.Name], idx.Sources[s.Name]
			if fp, err := it.Source.Fingerprint(ctx, root); err != nil {
				note(s.Name + ": " + err.Error())
			} else if valid {
				f := fp == s.Indexed
				s.Fresh = &f
			}
		}
		seen[s.Name] = true
		if s.Fresh == nil {
			known = false
		} else {
			fresh = fresh && *s.Fresh
		}
		st.Sources = append(st.Sources, s)
	}
	var gone []string
	for name := range idx.Sources {
		if !seen[name] {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	for _, name := range gone {
		if badGit && strings.HasPrefix(name, gitrepo.Name+":") {
			known = false
			continue
		}
		note("the index holds rules of " + name + ", which stele.json no longer lists")
		fresh = false
	}
	// The index is stale, as the hook sees it, when stele.lock no longer
	// agrees with it, whatever the fingerprints say (compile.EnsureFresh).
	if valid && !compile.LockAgrees(root, idx) {
		fresh = false
		if st.Project != "" && idx.Project != st.Project {
			note("the index is for project " + idx.Project + ", stele.lock for " + st.Project + ": run stele compile")
		} else {
			note("stele.lock no longer agrees with the index: run stele compile")
		}
	}
	if valid && (known || !fresh) {
		st.Index.Fresh = &fresh
	}
	return st, nil
}

func agentOf(root string, a adapter.Adapter) Agent {
	ag := Agent{Name: a.Name(), SettingsFile: filepath.ToSlash(a.SettingsFile()), RulesFile: filepath.ToSlash(a.RulesFile())}
	if v, sw, ok := install.Hooks(a, root); ok {
		ag.Hooks, ag.Installed, ag.Remind, ag.Check = true, v, !sw.RemindOff, !sw.CheckOff
	}
	if data, err := safefile.ReadRegular(filepath.Join(root, a.RulesFile()), a.MaxRulesFile()); err == nil {
		ag.RulesPresent = a.OwnedRules(data)
	}
	return ag
}

func pluginOf(ctx context.Context, it plugin.Item, in Input) Plugin {
	p := Plugin{Kind: it.Kind, Name: it.Name, Builtin: it.Builtin}
	if it.Builtin {
		t := it.Err == nil
		p.Found, p.Version = &t, in.Version
		if it.Err != nil {
			p.Error = it.Err.Error()
		}
		return p
	}
	if it.Err != nil {
		f := !errors.Is(it.Err, plugin.ErrPluginNotFound) && !errors.Is(it.Err, plugin.ErrPluginRefused)
		if in.Run != nil {
			p.Found = &f
		}
		p.Error = it.Err.Error()
		return p
	}
	if in.Run == nil {
		return p
	}
	info, err := plugin.Describe(ctx, in.Run, it.Kind, it.Name)
	found := err == nil || (!errors.Is(err, plugin.ErrPluginNotFound) && !errors.Is(err, plugin.ErrPluginRefused))
	p.Found = &found
	if err != nil {
		p.Error = err.Error()
	} else {
		p.Version = info.Version
	}
	return p
}

// loadIndex reads root's index: present is there being one, valid its
// signature verifying with key.
func loadIndex(root string, key sign.Key) (idx index.Index, present, valid bool) {
	if _, err := os.Lstat(index.Path(root)); errors.Is(err, fs.ErrNotExist) {
		return idx, false, false
	}
	if key == nil {
		return idx, true, false
	}
	idx, err := index.Load(root, key)
	return idx, true, err == nil
}
