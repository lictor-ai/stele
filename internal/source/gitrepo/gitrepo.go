// Package gitrepo is the built-in git source: rules from a stele.md in
// another git repository, read at a commit stele.lock pins (D71, D72,
// ADR-0011).
//
// It starts no process itself. git runs behind the Git interface, which
// cmd/stele fills with internal/gitexec, so nothing here reaches os/exec and
// the hook can import this package's registry without reaching git. The
// source runs only at stele compile, stele check and stele init: it is never
// one of plugin.Builtins(), the sources the hook refreshes (D45).
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/stelemd"
)

// Name is the source's name in stele.json.
const Name = "git"

// DefaultPath is the rules file read when "path" is unset.
const DefaultPath = "stele.md"

const (
	// maxFile caps the shared rules file, as stelemd caps stele.md.
	maxFile = 4 << 20
	// maxLock caps the guidelines repository's stele.lock.
	maxLock = 16 << 20
	// maxSetting caps "ref" and "path".
	maxSetting = 200
)

// Config is one "git" entry of the sources in stele.json.
type Config struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`  // a branch or tag; "" is the remote's default branch
	Path string `json:"path"` // the rules file in the repository; "" is DefaultPath
}

// Mode is how a read treats the pin in stele.lock.
type Mode int

const (
	// Pinned is stele compile and stele init: a pin that matches the
	// source's settings is kept; otherwise the ref is resolved again.
	Pinned Mode = iota
	// Frozen is stele check: a pin is never moved, and a source without
	// a matching pin is source.ErrUnpinned.
	Frozen
	// Update is stele compile --update: every ref is resolved again.
	Update
)

// Git is what the source needs of git: internal/gitexec.Fetcher.
type Git interface {
	// Remote checks repo and returns its label, host and path.
	Remote(repo string) (string, error)
	// Resolve returns the commit ref names now, fetched to read.
	Resolve(ctx context.Context, repo, ref string) (string, error)
	// Ensure makes the pinned commit readable, with no network call when it
	// already is.
	Ensure(ctx context.Context, repo, ref, commit string) error
	// File returns a plain file at path in commit, of at most max bytes, or
	// an fs.ErrNotExist error.
	File(ctx context.Context, repo, commit, path string, max int) ([]byte, error)
}

// Source is the git source for one entry of stele.json.
type Source struct {
	cfg   Config
	label string // host/path of the repository
	git   Git
	mode  Mode
}

// New checks cfg and returns its source. Nothing is fetched yet.
func New(cfg Config, git Git, mode Mode) (*Source, error) {
	if cfg.Path == "" {
		cfg.Path = DefaultPath
	}
	label, err := git.Remote(cfg.Repo)
	if err != nil {
		return nil, err
	}
	if err := ValidateRef(cfg.Ref); err != nil {
		return nil, err
	}
	if err := ValidatePath(cfg.Path); err != nil {
		return nil, err
	}
	return &Source{cfg: cfg, label: label, git: git, mode: mode}, nil
}

// Name is "git:" and the repository's host, path and the rules file in it:
// stable when only the ref changes.
func (s *Source) Name() string { return "git:" + s.label + "/" + s.cfg.Path }

// Label is the repository's host and path, and the rules file in it, for
// messages.
func (s *Source) Label() string { return s.label + "/" + s.cfg.Path }

// Config is the source's settings, with the default path filled in.
func (s *Source) Config() Config { return s.cfg }

// Read reads the rules at the commit stele.lock pins, as ReadPinned does
// with the lock's pins.
func (s *Source) Read(ctx context.Context, root string) ([]source.Rule, string, error) {
	f, err := lock.Load(root)
	if err != nil {
		return nil, "", err
	}
	rules, fp, _, err := s.ReadPinned(ctx, root, f.Sources)
	return rules, fp, err
}

// Fingerprint is the commit stele.lock pins, or "": it starts nothing. No
// one on the hot path calls it, as the source is never a builtin.
func (s *Source) Fingerprint(_ context.Context, root string) (string, error) {
	f, err := lock.Load(root)
	if err != nil {
		return "", err
	}
	if p, ok := s.pin(f.Sources); ok {
		return p.Commit, nil
	}
	return "", nil
}

// ReadPinned reads the shared rules at the commit the mode chooses, and
// returns the pin it read them at.
func (s *Source) ReadPinned(ctx context.Context, _ string, pins []source.Pin) ([]source.Rule, string, source.Pin, error) {
	p, matches := s.pin(pins)
	var commit string
	switch {
	case s.mode == Frozen && !matches:
		return nil, "", source.Pin{}, fmt.Errorf("%w; run stele compile and commit stele.lock", source.ErrUnpinned)
	case s.mode != Update && matches:
		if err := s.git.Ensure(ctx, s.cfg.Repo, s.cfg.Ref, p.Commit); err != nil {
			return nil, "", source.Pin{}, err
		}
		commit = p.Commit
	default:
		c, err := s.git.Resolve(ctx, s.cfg.Repo, s.cfg.Ref)
		if err != nil {
			return nil, "", source.Pin{}, err
		}
		commit = c
	}
	rules, err := s.rules(ctx, commit)
	if err != nil {
		return nil, "", source.Pin{}, err
	}
	pin := source.Pin{Source: s.Name(), Repo: s.cfg.Repo, Ref: s.cfg.Ref, Path: s.cfg.Path, Commit: commit}
	return rules, commit, pin, nil
}

// pin returns stele.lock's pin for this source, and whether it was made
// with the settings stele.json has now.
func (s *Source) pin(pins []source.Pin) (source.Pin, bool) {
	for _, p := range pins {
		if p.Source == s.Name() {
			return p, p.Repo == s.cfg.Repo && p.Ref == s.cfg.Ref && p.Path == s.cfg.Path
		}
	}
	return source.Pin{}, false
}

// rules reads and parses the rules file at commit, with the ids the
// guidelines repository's own stele.lock gives them.
func (s *Source) rules(ctx context.Context, commit string) ([]source.Rule, error) {
	data, err := s.git.File(ctx, s.cfg.Repo, commit, s.cfg.Path, maxFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s at %s", s.cfg.Repo, s.cfg.Path, short(commit))
	}
	if err != nil {
		return nil, err
	}
	rules, err := stelemd.ParseNamed(data, s.Label()+"@"+short(commit))
	if err != nil {
		return nil, err
	}
	for i := range rules {
		rules[i].Source = s.Name()
	}
	if err := s.supplyIDs(ctx, commit, rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// canonicalID is a UUID as stele mints it. A supplied id goes into the
// generated rules file, so nothing else is accepted.
var canonicalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// supplyIDs gives each rule the id the guidelines repository's stele.lock,
// beside a shared stele.md, holds for it (D52): same body and position
// first, then same body. A rule it does not list gets an id in this
// project's lock. A file not named stele.md has no lock beside it.
func (s *Source) supplyIDs(ctx context.Context, commit string, rules []source.Rule) error {
	if path.Base(s.cfg.Path) != DefaultPath {
		return nil
	}
	lockPath := path.Join(path.Dir(s.cfg.Path), lock.FileName)
	data, err := s.git.File(ctx, s.cfg.Repo, commit, lockPath, maxLock)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := lock.Decode(data)
	if err != nil {
		return fmt.Errorf("%s/%s@%s: %w", s.label, lockPath, short(commit), err)
	}
	var entries []lock.Entry
	for _, e := range f.Rules {
		if e.Source != "stelemd" {
			continue
		}
		if !canonicalID.MatchString(e.ID) {
			return fmt.Errorf("%s/%s@%s: %w: id %q is not a UUID", s.label, lockPath, short(commit), lock.ErrInvalid, e.ID)
		}
		entries = append(entries, e)
	}
	used := make([]bool, len(entries))
	claim := func(match func(lock.Entry) bool) string {
		for j, e := range entries {
			if !used[j] && match(e) {
				used[j] = true
				return e.ID
			}
		}
		return ""
	}
	hashes := make([]string, len(rules))
	for i, r := range rules {
		hashes[i] = source.Hash(r.Body)
		rules[i].ID = claim(func(e lock.Entry) bool { return e.Hash == hashes[i] && e.Position == r.Position })
	}
	for i := range rules {
		if rules[i].ID == "" {
			rules[i].ID = claim(func(e lock.Entry) bool { return e.Hash == hashes[i] })
		}
	}
	return nil
}

// refPart is what one part of a ref may hold: git's rules for a ref name,
// narrowed to what a branch or tag is usually called.
var refPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)

// ValidateRef accepts "", a branch or tag name, or refs/heads/... or
// refs/tags/...: parts of letters, digits and . _ + -, as git check-ref-format
// would, never an option.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	bad := func(why string) error { return fmt.Errorf("ref %q: %s", ref, why) }
	if len(ref) > maxSetting {
		return bad(fmt.Sprintf("over %d bytes", maxSetting))
	}
	if strings.HasPrefix(ref, "refs/") && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		return bad("a full name starts with refs/heads/ or refs/tags/")
	}
	for _, part := range strings.Split(ref, "/") {
		switch {
		case !refPart.MatchString(part):
			return bad("not a branch or tag name")
		case strings.Contains(part, ".."), strings.HasSuffix(part, "."), strings.HasSuffix(part, ".lock"):
			return bad("not a branch or tag name")
		}
	}
	return nil
}

// pathPart is one part of "path".
var pathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ValidatePath accepts a relative, slash-separated path ending in .md, of
// letters, digits and . _ -.
func ValidatePath(p string) error {
	bad := func(why string) error { return fmt.Errorf("path %q: %s", p, why) }
	switch {
	case len(p) > maxSetting:
		return bad(fmt.Sprintf("over %d bytes", maxSetting))
	case !strings.HasSuffix(p, ".md"):
		return bad("must end in .md")
	case strings.ContainsFunc(p, unicode.IsSpace):
		return bad("has a space")
	}
	for _, part := range strings.Split(p, "/") {
		if !pathPart.MatchString(part) || strings.Contains(part, "..") {
			return bad("each part is letters, digits, '.', '_' and '-', and starts with a letter, a digit or '_'")
		}
	}
	return nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
