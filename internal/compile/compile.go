// Package compile is the one place that turns sources into an index.
//
// Compiling reads every source, reconciles the rules against stele.lock, and
// builds the index. Plan does all of that and writes nothing, which is what
// stele check needs. Apply writes the lock, the index, and the files
// generated from the index for an agent to load. EnsureFresh is the quick
// check a coding session makes: built-in sources only (D45), and only those
// whose fingerprint changed.
package compile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
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
	// Files are the generated files, in the order they were asked for.
	Files []File
}

// Generated is a file compile writes from the index for an agent to load,
// such as the rules file an agent loads (#68). compile knows its path and its
// bytes, never its format.
type Generated struct {
	Path   string // relative to the root, slash-separated
	Render func(index.Index) []byte
	// Owned reports whether a file found at Path is one compile wrote. One
	// that is not is never overwritten. nil: any file is.
	Owned func(disk []byte) bool
	// Same reports whether the file on disk is up to date with want. nil:
	// the same text, line ends and a byte order mark aside.
	Same func(disk, want []byte) bool
}

// File is a generated file as planned.
type File struct {
	Path string
	Data []byte
	// Changed says the file on disk is out of date, or missing.
	Changed bool
	// Foreign says something compile did not write is at Path: a file
	// Owned rejects, or anything it cannot read as one. It is left alone.
	Foreign bool
}

// GeneratedError is a generated file that could not be written. Apply
// returns it after the lock and the index are saved, so the rules still
// apply: only the file an agent loads is out of date.
type GeneratedError struct {
	Path string
	Err  error
}

func (e *GeneratedError) Error() string { return fmt.Sprintf("writing %s: %v", e.Path, e.Err) }
func (e *GeneratedError) Unwrap() error { return e.Err }

// maxGenerated caps a read of a generated file to compare it.
const maxGenerated = 16 << 20

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
func Plan(ctx context.Context, root string, srcs []source.Source, gens []Generated, newID func() string) (Result, error) {
	rules, fps, err := readAll(ctx, root, srcs)
	if err != nil {
		return Result{}, err
	}
	return plan(root, rules, fps, gens, newID, false, nil)
}

// Compile is stele compile: it reads every source, then plans and applies
// under the compile lock, so a concurrent EnsureFresh never interleaves its
// writes with these.
func Compile(ctx context.Context, root string, srcs []source.Source, gens []Generated, newID func() string, key sign.Key) (Result, error) {
	rules, fps, err := readAll(ctx, root, srcs)
	if err != nil {
		return Result{}, err
	}
	unlock, err := lockCompile(root, lockWait)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	r, err := plan(root, rules, fps, gens, newID, false, key)
	if err != nil {
		return Result{}, err
	}
	return r, Apply(root, r, key)
}

// Apply writes what changed: the lock, then the index, then the generated
// files. The index holds ids from the lock, so writing the lock first means a
// crash between the two leaves an index that the next compile rebuilds, never
// a lost id; the generated files come from the index. A caller that plans and
// applies should hold the compile lock across both, as Compile and
// EnsureFresh do.
func Apply(root string, r Result, key sign.Key) error {
	if r.LockChanged {
		if err := lock.Save(root, r.Lock); err != nil {
			return err
		}
	}
	if r.IndexChanged {
		if err := index.Save(root, r.Index, key); err != nil {
			return err
		}
	}
	return writeGenerated(root, r.Files, safefile.Replace)
}

// writeGenerated writes the files that changed, through replace. Every
// directory on the way must be a plain one: a clone can commit a link there,
// and stele would then write outside the repository.
func writeGenerated(root string, files []File, replace func(string, []byte) error) error {
	for _, f := range files {
		if !f.Changed || f.Foreign {
			continue
		}
		parts := strings.Split(f.Path, "/")
		dir := root
		for _, p := range parts[:len(parts)-1] {
			dir = filepath.Join(dir, p)
			if err := safefile.Dir(dir); err != nil {
				return &GeneratedError{f.Path, err}
			}
		}
		if err := replace(filepath.Join(root, filepath.FromSlash(f.Path)), f.Data); err != nil {
			return &GeneratedError{f.Path, err}
		}
	}
	return nil
}

// generate renders each generated file from idx and compares it with the
// one on disk.
func generate(root string, idx index.Index, gens []Generated) []File {
	files := make([]File, len(gens))
	for i, g := range gens {
		data := g.Render(idx)
		f := File{Path: g.Path, Data: data}
		disk, err := safefile.ReadRegular(filepath.Join(root, filepath.FromSlash(g.Path)), maxGenerated)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.Changed = true
		case err != nil, g.Owned != nil && !g.Owned(disk):
			// A link, a directory, a file too large to read, or one without
			// its header: not compile's to replace.
			f.Foreign = true
		case g.Same != nil:
			f.Changed = !g.Same(disk, data)
		default:
			f.Changed = !sameText(disk, data)
		}
		files[i] = f
	}
	return files
}

// sameText reports whether a and b are the same text, as a checkout may
// change line ends and add a byte order mark.
func sameText(a, b []byte) bool {
	norm := func(s []byte) []byte {
		s = bytes.TrimPrefix(s, []byte("\xef\xbb\xbf"))
		return bytes.ReplaceAll(s, []byte("\r\n"), []byte("\n"))
	}
	return bytes.Equal(norm(a), norm(b))
}

// staleGenerated reports whether a generated file differs from what idx
// gives.
func staleGenerated(root string, idx index.Index, gens []Generated) bool {
	for _, f := range generate(root, idx, gens) {
		if f.Changed {
			return true
		}
	}
	return false
}

// EnsureFresh re-reads only builtins, and only those whose fingerprint
// changed, or all of them when stele.lock no longer agrees with the index.
// Rules from every other source are carried over from the current index. With
// no usable index it builds one from the builtins alone; external rules come
// back at the next stele compile. The bool says whether it recompiled.
//
// It never reads an external source, so it never drops one's lock entries
// either: their rules keep their ids for the next stele compile, even when
// the index no longer holds them.
//
// A generated file can be stale while the index is fresh, after a branch
// switch or a deletion: then only the file is rewritten, and only when that
// is cheap. The hook runs on every write, so it never waits for the compile
// lock or for a reader to close the file there, and a refresh that cannot
// be made at once is skipped, not an error: the index serves the rules, and
// stele check reports the file.
func EnsureFresh(ctx context.Context, root string, builtins []source.Source, gens []Generated, newID func() string, key sign.Key) (index.Index, bool, error) {
	cur, ok, stale, err := staleness(ctx, root, builtins, key)
	if err != nil || (ok && len(stale) == 0 && !staleGenerated(root, cur, gens)) {
		return cur, false, err
	}
	if ok && len(stale) == 0 {
		refresh(ctx, root, builtins, gens, key)
		return cur, false, nil
	}

	unlock, err := lockCompile(root, lockWait)
	if err != nil {
		return index.Index{}, false, err
	}
	defer unlock()
	// Another process may have rebuilt while this one waited for the lock.
	cur, ok, stale, err = staleness(ctx, root, builtins, key)
	if err != nil {
		return cur, false, err
	}
	if ok && len(stale) == 0 {
		return cur, false, writeGenerated(root, generate(root, cur, gens), safefile.Replace)
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

	r, err := plan(root, rules, fps, gens, newID, true, key)
	if err != nil {
		return index.Index{}, false, err
	}
	return r.Index, true, Apply(root, r, key)
}

// staleness loads the index and says which builtins changed since it was
// built. A stele.lock the index no longer agrees with makes every builtin
// stale, so the rebuild takes its ids from that lock. ok is false when there
// is no usable index; cur is then empty.
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
	if !lockAgrees(root, cur) {
		for _, b := range builtins {
			stale[b.Name()] = true
		}
	}
	return cur, true, stale, nil
}

// lockAgrees reports whether stele.lock gives the index's project and each of
// its rules the identity the index holds. A pull can change the lock while
// stele.md stays the same. Entries the index does not hold, such as an
// external source's, say nothing. Neither does a lock that is missing or
// invalid: the index serves the rules, and stele check reports the lock.
func lockAgrees(root string, idx index.Index) bool {
	f, err := lock.Load(root)
	if err != nil || f.Project == "" {
		return true
	}
	if f.Project != idx.Project {
		return false
	}
	entries := make(map[string]lock.Entry, len(f.Rules))
	for _, e := range f.Rules {
		entries[e.ID] = e
	}
	for _, r := range idx.Rules {
		e, ok := entries[r.ID]
		if !ok || e.Source != r.Source || e.Hash != r.BodyHash {
			return false
		}
	}
	return true
}

// refresh rewrites stale generated files while the index is fresh, if it can
// at once: the compile lock taken without waiting, the file replaced without
// waiting for a reader. Anything else is a skip.
func refresh(ctx context.Context, root string, builtins []source.Source, gens []Generated, key sign.Key) {
	unlock, err := lockCompile(root, 0)
	if err != nil {
		return
	}
	defer unlock()
	cur, ok, stale, err := staleness(ctx, root, builtins, key)
	if err != nil || !ok || len(stale) > 0 {
		return // another process is rebuilding; it writes the files too
	}
	_ = writeGenerated(root, generate(root, cur, gens), safefile.ReplaceNow)
}

// lockCompile takes the compile lock under .stele/, waiting at most wait.
func lockCompile(root string, wait time.Duration) (func(), error) {
	if err := index.EnsureDir(root); err != nil {
		return nil, err
	}
	return safefile.Lock(filepath.Join(root, index.Dir, lockName), wait, lockStale)
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
func plan(root string, rules []source.Rule, fps map[string]string, gens []Generated, newID func() string, keepUnread bool, key sign.Key) (Result, error) {
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
		Files:        generate(root, idx, gens),
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
