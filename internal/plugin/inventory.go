package plugin

import (
	"fmt"
	"sort"

	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// Item is one plugin of the install: built in, or listed in stele.json.
type Item struct {
	Kind    Kind
	Name    string // a git source's is its source name, git:<host>/<path>
	Builtin bool   // compiled in: stelemd, regex, and the git source
	// Source is the source, for a source stele could build: a built-in, a
	// git source, or an external one found with the runner. A git source
	// built here reads nothing and moves no pin.
	Source source.Source
	// Err is what is wrong with the entry: a bad entry, or the plugin not
	// found or refused.
	Err error
}

// Inventory lists every plugin of the install under root, in order: the
// sources, then the checkers. It starts nothing; with run it finds each
// external plugin on PATH, and an external source found is built, so its
// fingerprint can be asked. Without run nothing is looked up. A bad entry or
// a missing plugin is an Item with its Err; only a stele.json that cannot be
// read or decoded is an error. stele status calls it (D85).
func Inventory(root string, git gitrepo.Git, run Runner) ([]Item, error) {
	srcEntries, err := listed(root, "sources", ErrSources)
	if err != nil {
		return nil, err
	}
	chkEntries, err := listed(root, "checkers", ErrCheckers)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, s := range Builtins() {
		items = append(items, Item{Kind: KindSource, Name: s.Name(), Builtin: true, Source: s})
	}
	for i, e := range srcEntries {
		if e.Name == gitrepo.Name {
			it := Item{Kind: KindSource, Name: gitrepo.Name, Builtin: true}
			var cfg gitrepo.Config
			if err := strict(e.Config, &cfg); err != nil {
				it.Err = fmt.Errorf("%w: sources[%d]: config: %v", ErrSources, i, err)
			} else if s, err := gitrepo.New(cfg, git, gitrepo.Frozen); err != nil {
				it.Err = fmt.Errorf("%w: sources[%d]: %v", ErrSources, i, err)
			} else {
				it.Name, it.Source = s.Name(), s
			}
			items = append(items, it)
			continue
		}
		it := Item{Kind: KindSource, Name: e.Name}
		if run == nil {
			it.Err = ValidName(e.Name)
		} else if p, err := external(KindSource, "sources", e, ReadTimeout, run); err != nil {
			it.Err = err
		} else {
			it.Source = &execSource{name: p.name, bin: p.bin, config: p.config, timeout: p.timeout, run: run}
		}
		items = append(items, it)
	}
	var builtin []string
	for name := range BuiltinCheckers() {
		builtin = append(builtin, name)
	}
	sort.Strings(builtin)
	for _, name := range builtin {
		items = append(items, Item{Kind: KindChecker, Name: name, Builtin: true})
	}
	for _, e := range chkEntries {
		it := Item{Kind: KindChecker, Name: e.Name}
		if run == nil {
			it.Err = ValidName(e.Name)
		} else if _, err := external(KindChecker, "checkers", e, CheckTimeout, run); err != nil {
			it.Err = err
		}
		items = append(items, it)
	}
	return items, nil
}
