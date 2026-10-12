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
	"path/filepath"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
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

const (
	// lockName serialises compiles: stele compile, and EnsureFresh in each
	// stele mcp. Without it two of them can each mint an id for the same new
	// rule and leave stele.lock and the index disagreeing.
	lockName = "compile.lock"
	// lockWait is how long a compile waits for another. Only the lock and
	// the index are written under it, never a source read, so it is short.
	lockWait = 5 * time.Second
	// lockStale is the age past which the lock was left by a crash.
	lockStale = 30 * time.Second
)

// Plan reads every source and writes nothing. It holds no key, so it trusts
// no index on disk and IndexChanged is always true: stele check, its caller,
// never serves the index and never writes, so it needs neither.
func Plan(ctx context.Context, root string, srcs []source.Source, newID func() string) (Result, error) {
	rules, fps, err := readAll(ctx, root, srcs)
	if err != nil {
		return Result{}, err
	}
	return plan(root, rules, fps, newID, false, nil)
}

// Compile is stele compile: it reads every source, then plans and applies
// under the compile lock, so a concurrent EnsureFresh never interleaves its
// writes with these.
func Compile(ctx context.Context, root string, srcs []source.Source, newID func() string, key sign.Key) (Result, error) {
	rules, fps, err := readAll(ctx, root, srcs)
	if err != nil {
		return Result{}, err
	}
	unlock, err := lockCompile(root)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	r, err := plan(root, rules, fps, newID, false, key)
	if err != nil {
		return Result{}, err
	}
	return r, Apply(root, r, key)
}

// Apply writes what changed: the lock, then the index. The index holds ids
// from the lock, so writing the lock first means a crash between the two
// leaves an index that the next compile rebuilds, never a lost id. A caller
// that plans and applies should hold the compile lock across both, as
// Compile and EnsureFresh do.
func Apply(root string, r Result, key sign.Key) error {
	if r.LockChanged {
		if err := lock.Save(root, r.Lock); err != nil {
			return err
		}
	}
	if r.IndexChanged {
		return index.Save(root, r.Index, key)
	}
	return nil
}

// EnsureFresh re-reads only builtins, and only those whose fingerprint
// changed. Rules from every other source are carried over from the current
// index. With no usable index it builds one from the builtins alone; external
// rules come back at the next stele compile. The bool says whether it
// recompiled.
//
// It never reads an external source, so it never drops one's lock entries
// either: their rules keep their ids for the next stele compile, even when
// the index no longer holds them.
func EnsureFresh(ctx context.Context, root string, builtins []source.Source, newID func() string, key sign.Key) (index.Index, bool, error) {
	cur, ok, stale, err := staleness(ctx, root, builtins, key)
	if err != nil || (ok && len(stale) == 0) {
		return cur, false, err
	}

	unlock, err := lockCompile(root)
	if err != nil {
		return index.Index{}, false, err
	}
	defer unlock()
	// Another process may have rebuilt while this one waited for the lock.
	cur, ok, stale, err = staleness(ctx, root, builtins, key)
	if err != nil || (ok && len(stale) == 0) {
		return cur, false, err
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
		if ok && !stale[b.Name()] {
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

	r, err := plan(root, rules, fps, newID, true, key)
	if err != nil {
		return index.Index{}, false, err
	}
	return r.Index, true, Apply(root, r, key)
}

// staleness loads the index and says which builtins changed since it was
// built. ok is false when there is no usable index; cur is then empty.
func staleness(ctx context.Context, root string, builtins []source.Source, key sign.Key) (cur index.Index, ok bool, stale map[string]bool, err error) {
	cur, err = index.Load(root, key)
	if errors.Is(err, index.ErrNoIndex) {
		return index.Index{}, false, nil, nil
	}
	if err != nil {
		return index.Index{}, false, nil, err
	}
	stale = make(map[string]bool)
	for _, b := range builtins {
		fp, err := b.Fingerprint(ctx, root)
		if err != nil {
			return index.Index{}, false, nil, fmt.Errorf("source %s: %w", b.Name(), err)
		}
		if old, found := cur.Sources[b.Name()]; !found || old != fp {
			stale[b.Name()] = true
		}
	}
	return cur, true, stale, nil
}

// lockCompile takes the compile lock under .stele/.
func lockCompile(root string) (func(), error) {
	if err := index.EnsureDir(root); err != nil {
		return nil, err
	}
	return safefile.Lock(filepath.Join(root, index.Dir, lockName), lockWait, lockStale)
}

// readAll reads every source, in order.
func readAll(ctx context.Context, root string, srcs []source.Source) ([]source.Rule, map[string]string, error) {
	var rules []source.Rule
	fps := make(map[string]string, len(srcs))
	for _, s := range srcs {
		rs, fp, err := read(ctx, root, s)
		if err != nil {
			return nil, nil, err
		}
		rules = append(rules, rs...)
		fps[s.Name()] = fp
	}
	return rules, fps, nil
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
func plan(root string, rules []source.Rule, fps map[string]string, newID func() string, keepUnread bool, key sign.Key) (Result, error) {
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

	cur, err := index.Load(root, key)
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
