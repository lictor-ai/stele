// Package compile is the one place that turns sources into an index.
//
// Compiling reads every source, reconciles the rules against stele.lock, and
// builds the index. Plan does all of that and writes nothing, which is what
// stele check needs. Apply writes the lock and the index. EnsureFresh is the
// quick check a coding session makes: built-in sources only (D45), and only
// those whose fingerprint changed.
package compile

import (
	"context"
	"errors"
	"fmt"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
)

// Result is a planned compile.
type Result struct {
	Index  index.Index
	Lock   lock.File
	Report lock.Report
	// LockChanged says the planned lock differs from stele.lock on disk. For
	// stele check that means the lock is out of date.
	LockChanged bool
	// IndexChanged says the planned index differs from the one on disk, or
	// there is no usable index.
	IndexChanged bool
}

// Plan reads every source and writes nothing.
func Plan(ctx context.Context, root string, srcs []source.Source, newID func() string) (Result, error) {
	var rules []source.Rule
	fps := make(map[string]string, len(srcs))
	for _, s := range srcs {
		rs, fp, err := read(ctx, root, s)
		if err != nil {
			return Result{}, err
		}
		rules = append(rules, rs...)
		fps[s.Name()] = fp
	}
	return plan(root, rules, fps, newID, false)
}

// Apply writes what changed: the lock, then the index. The index holds ids
// from the lock, so writing the lock first means a crash between the two
// leaves an index that the next compile rebuilds, never a lost id.
func Apply(root string, r Result) error {
	if r.LockChanged {
		if err := lock.Save(root, r.Lock); err != nil {
			return err
		}
	}
	if r.IndexChanged {
		return index.Save(root, r.Index)
	}
	return nil
}

// EnsureFresh re-reads only builtins, and only those whose fingerprint
// changed. Rules from every other source are carried over from the current
// index. With no usable index it builds one from the builtins alone; external
// rules come back at the next stele compile. The bool says whether it
// recompiled.
func EnsureFresh(ctx context.Context, root string, builtins []source.Source, newID func() string) (index.Index, bool, error) {
	cur, err := index.Load(root)
	if errors.Is(err, index.ErrNoIndex) {
		var rules []source.Rule
		fps := make(map[string]string, len(builtins))
		for _, b := range builtins {
			rs, fp, err := read(ctx, root, b)
			if err != nil {
				return index.Index{}, false, err
			}
			rules = append(rules, rs...)
			fps[b.Name()] = fp
		}
		r, err := plan(root, rules, fps, newID, true)
		if err != nil {
			return index.Index{}, false, err
		}
		return r.Index, true, Apply(root, r)
	}
	if err != nil {
		return index.Index{}, false, err
	}

	stale := make(map[string]bool)
	for _, b := range builtins {
		fp, err := b.Fingerprint(ctx, root)
		if err != nil {
			return index.Index{}, false, fmt.Errorf("source %s: %w", b.Name(), err)
		}
		if old, ok := cur.Sources[b.Name()]; !ok || old != fp {
			stale[b.Name()] = true
		}
	}
	if len(stale) == 0 {
		return cur, false, nil
	}

	// Rules keep the order Plan gives them: builtins first, then the rest in
	// index order.
	var rules []source.Rule
	fps := make(map[string]string, len(cur.Sources))
	for name, fp := range cur.Sources {
		fps[name] = fp
	}
	builtin := make(map[string]bool, len(builtins))
	for _, b := range builtins {
		builtin[b.Name()] = true
		if !stale[b.Name()] {
			rules = append(rules, carried(cur, b.Name())...)
			continue
		}
		rs, fp, err := read(ctx, root, b)
		if err != nil {
			return index.Index{}, false, err
		}
		rules = append(rules, rs...)
		fps[b.Name()] = fp
	}
	for _, e := range cur.Rules {
		if !builtin[e.Source] {
			rules = append(rules, rule(e))
		}
	}

	r, err := plan(root, rules, fps, newID, false)
	if err != nil {
		return index.Index{}, false, err
	}
	return r.Index, true, Apply(root, r)
}

// read reads one source. A parse error already names its file and line, so
// only other errors are prefixed with the source.
func read(ctx context.Context, root string, s source.Source) ([]source.Rule, string, error) {
	rules, fp, err := s.Read(ctx, root)
	if err != nil {
		var pe *source.ParseError
		if !errors.As(err, &pe) {
			err = fmt.Errorf("source %s: %w", s.Name(), err)
		}
		return nil, "", err
	}
	return rules, fp, nil
}

// plan reconciles rules against stele.lock and builds the index. keepUnread
// keeps the lock entries of sources that were not read, so a rebuild from the
// builtins alone does not cost external rules their ids.
func plan(root string, rules []source.Rule, fps map[string]string, newID func() string, keepUnread bool) (Result, error) {
	prev, err := lock.Load(root)
	if err != nil {
		return Result{}, err
	}
	ids, next, rep, err := lock.Reconcile(rules, prev, newID)
	if err != nil {
		return Result{}, err
	}
	if keepUnread {
		used := make(map[string]bool, len(ids))
		for _, id := range ids {
			used[id] = true
		}
		for _, e := range prev.Rules {
			if _, read := fps[e.Source]; !read && !used[e.ID] {
				next.Rules = append(next.Rules, e)
				rep.Dropped--
			}
		}
	}

	idx := index.Index{
		Version: index.Version,
		Project: next.Project,
		Sources: fps,
		Rules:   make([]index.Entry, len(rules)),
	}
	for i, r := range rules {
		idx.Rules[i] = index.Entry{
			ID:          ids[i],
			Source:      r.Source,
			Position:    r.Position,
			Description: r.Description,
			Tags:        r.Tags,
			Scope:       r.Scope,
			Meta:        r.Meta,
			Body:        r.Body,
			BodyHash:    next.Rules[i].Hash,
		}
	}

	cur, err := index.Load(root)
	if err != nil && !errors.Is(err, index.ErrNoIndex) {
		return Result{}, err
	}
	return Result{
		Index:        idx,
		Lock:         next,
		Report:       rep,
		LockChanged:  !lock.Equal(prev, next),
		IndexChanged: err != nil || !index.Equal(cur, idx),
	}, nil
}

// carried returns the index's rules from one source, in index order.
func carried(idx index.Index, src string) []source.Rule {
	var rules []source.Rule
	for _, e := range idx.Rules {
		if e.Source == src {
			rules = append(rules, rule(e))
		}
	}
	return rules
}

// rule turns an index entry back into the rule it was compiled from. The id
// is left out: the lock gives it back by hash, as for any rule.
func rule(e index.Entry) source.Rule {
	return source.Rule{
		Description: e.Description,
		Tags:        e.Tags,
		Scope:       e.Scope,
		Body:        e.Body,
		Meta:        e.Meta,
		Source:      e.Source,
		Position:    e.Position,
	}
}
