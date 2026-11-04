package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lictor-ai/stele/internal/safefile"
)

// ErrPinGone is a pinned commit that is not on its ref: gone after a force
// push, or never on it. A commit the remote serves by id but not on the ref,
// such as a fork's pull request, is one: a pin is good only on its ref.
var ErrPinGone = errors.New("not on its ref")

// fetchFlags go on every git command the Fetcher runs: objects that come
// from a remote are checked.
var fetchFlags = []string{"-c", "transfer.fsckObjects=true"}

// fetchNetTimeout bounds one Fetcher command that talks to the remote:
// stele compile and stele check wait on it.
const fetchNetTimeout = 5 * time.Minute

const (
	// cacheWait is how long a Fetcher waits for another stele using the same
	// repository's cache.
	cacheWait = 2 * time.Minute
	// cacheStale is the age at which a cache lock was left by a crash: more
	// than three network commands.
	cacheStale = time.Hour
)

// Fetcher reads files of another repository at a commit, for the git source
// (ADR-0011). It keeps one bare clone per repository in the user's cache
// directory, never inside the project, and checks nothing out, so no file of
// the repository is written as a file and none of its hooks runs. A commit
// already there is read with no network call.
type Fetcher struct {
	// Root is the project: git is found on PATH outside it, and gets no PATH
	// entry inside it.
	Root string
	// CacheDir is the user's cache directory. Tests replace it.
	CacheDir func() (string, error)
	// Remotes, set only by tests, in code, maps a repo to a bare repository
	// on disk that git reads instead, with the file transport allowed.
	Remotes map[string]string

	git *Cmd
}

// NewFetcher is a Fetcher for the project at root.
func NewFetcher(root string) *Fetcher {
	return &Fetcher{Root: root, CacheDir: os.UserCacheDir}
}

// Remote checks repo and returns its label: host and path, without .git.
func (f *Fetcher) Remote(repo string) (string, error) {
	if err := ValidateRepo(repo); err != nil {
		return "", err
	}
	host, path := HostPath(repo)
	return host + "/" + path, nil
}

// commitHex is a git commit id.
var commitHex = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// checkRef refuses a ref git could read as an option or that is not one
// name. The git source checks the rest of git's rules.
func checkRef(ref string) error {
	if strings.HasPrefix(ref, "-") || strings.ContainsFunc(ref, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("ref %q is not a branch or tag name", ref)
	}
	return nil
}

// Resolve returns the commit ref names on repo, "" being its default
// branch, and fetches it into the cache.
func (f *Fetcher) Resolve(ctx context.Context, repo, ref string) (string, error) {
	if err := checkRef(ref); err != nil {
		return "", err
	}
	if err := ValidateRepo(repo); err != nil {
		return "", err
	}
	g, url, err := f.cmd(repo)
	if err != nil {
		return "", err
	}
	dir, unlock, err := f.lock(repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	return f.resolve(ctx, g, dir, repo, url, ref)
}

// Ensure makes sure commit, pinned from ref on repo, is in the cache and on
// the ref: the ref's tip or a commit before it. When the cache already holds
// both, it makes no network call. Otherwise it fetches the ref, and a commit
// still not on it is ErrPinGone: stele never fetches a commit by its id.
func (f *Fetcher) Ensure(ctx context.Context, repo, ref, commit string) error {
	if !commitHex.MatchString(commit) {
		return fmt.Errorf("pinned commit %q is not a git commit id", commit)
	}
	if err := checkRef(ref); err != nil {
		return err
	}
	if err := ValidateRepo(repo); err != nil {
		return err
	}
	g, url, err := f.cmd(repo)
	if err != nil {
		return err
	}
	dir, unlock, err := f.lock(repo)
	if err != nil {
		return err
	}
	defer unlock()
	if onRef(ctx, g, dir, ref, commit) {
		return nil
	}
	if _, err := f.resolve(ctx, g, dir, repo, url, ref); err != nil {
		return err
	}
	if onRef(ctx, g, dir, ref, commit) {
		return nil
	}
	name := ref
	if name == "" {
		name = "the default branch"
	}
	return fmt.Errorf("pinned commit %s of %s is %w as %s: run stele compile --update to pin its tip", short(commit), Redact(repo), ErrPinGone, name)
}

// File returns the file at path in commit, a plain file of at most max
// bytes. A missing path is fs.ErrNotExist. A link or a submodule is
// refused: git would never write either as this file. It makes no network
// call: Resolve or Ensure brought the commit.
func (f *Fetcher) File(ctx context.Context, repo, commit, path string, max int) ([]byte, error) {
	if !commitHex.MatchString(commit) {
		return nil, fmt.Errorf("commit %q is not a git commit id", commit)
	}
	if path == "" || strings.HasPrefix(path, "-") || strings.HasPrefix(path, "/") || strings.ContainsFunc(path, unicode.IsControl) {
		return nil, fmt.Errorf("path %q is not a file in a repository", path)
	}
	g, _, err := f.cmd(repo)
	if err != nil {
		return nil, err
	}
	dir, err := f.dir(repo)
	if err != nil {
		return nil, err
	}
	out, err := bareRun(ctx, g, dir, "ls-tree", "-z", "--full-tree", commit, "--", path)
	if err != nil {
		return nil, err
	}
	entry, _, _ := strings.Cut(out, "\x00")
	meta, name, ok := strings.Cut(entry, "\t")
	if !ok || name != path {
		return nil, fmt.Errorf("%s at %s: %w", path, short(commit), fs.ErrNotExist)
	}
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, fmt.Errorf("%s at %s is not a plain file (a link or a submodule?)", path, short(commit))
	}
	oid := fields[2]
	size, err := bareRun(ctx, g, dir, "cat-file", "-s", oid)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(size))
	if err != nil {
		return nil, fmt.Errorf("%s at %s: size %q", path, short(commit), size)
	}
	if n > max {
		return nil, fmt.Errorf("%s at %s is over %d bytes", path, short(commit), max)
	}
	data, err := g.RunLimit(ctx, dir, nil, nil, max+1, "--git-dir="+dir, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// resolve reads ref's commit with ls-remote, clones repo into dir when
// there is no clone yet, and fetches the ref. The caller holds dir's lock.
func (f *Fetcher) resolve(ctx context.Context, g Cmd, dir, repo, url, ref string) (string, error) {
	full, commit, err := lsRemote(ctx, g, url, ref)
	if err != nil {
		return "", access(err, repo)
	}
	if !bareRepo(ctx, g, dir) {
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		// clone, unlike init, takes the remote's object format.
		short := strings.TrimPrefix(strings.TrimPrefix(full, "refs/heads/"), "refs/tags/")
		if _, err := g.Run(ctx, filepath.Dir(dir), nil, nil, append(fetchFlags, "clone", "--bare", "--single-branch", "--branch", short, "--no-tags", "--quiet", "--", url, dir)...); err != nil {
			return "", access(err, repo)
		}
	}
	local := localRef(ref)
	if _, err := bareRun(ctx, g, dir, "fetch", "--no-tags", "--no-recurse-submodules", "--quiet", "--", url, "+"+full+":"+local); err != nil {
		return "", access(err, repo)
	}
	if !has(ctx, g, dir, commit) {
		return "", fmt.Errorf("%s: %s names %s, which the fetch did not bring", repo, full, short(commit))
	}
	return commit, nil
}

// lsRemote returns the full name of ref on the remote and its commit, a
// tag peeled to the commit it names. "" is the default branch.
func lsRemote(ctx context.Context, g Cmd, url, ref string) (full, commit string, err error) {
	var want []string
	switch {
	case ref == "":
		want = []string{"HEAD"}
	case strings.HasPrefix(ref, "refs/"):
		want = []string{ref, ref + "^{}"}
	default:
		want = []string{"refs/heads/" + ref, "refs/tags/" + ref, "refs/tags/" + ref + "^{}"}
	}
	out, err := g.Run(ctx, os.TempDir(), nil, nil, append(append(fetchFlags, "ls-remote", "--symref", "--", url), want...)...)
	if err != nil {
		return "", "", err
	}
	oids := map[string]string{}
	head := ""
	for _, line := range strings.Split(out, "\n") {
		left, right, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if target, ok := strings.CutPrefix(left, "ref: "); ok {
			if right == "HEAD" {
				head = target
			}
			continue
		}
		oids[right] = left
	}
	pick := func(name string) (string, string, bool) {
		if c, ok := oids[name+"^{}"]; ok {
			return name, c, true
		}
		c, ok := oids[name]
		return name, c, ok
	}
	switch {
	case ref == "":
		if head == "" || oids["HEAD"] == "" {
			return "", "", errors.New("the repository has no default branch")
		}
		return head, oids["HEAD"], nil
	case strings.HasPrefix(ref, "refs/"):
		if n, c, ok := pick(ref); ok {
			return n, c, nil
		}
	default:
		branch, isBranch := oids["refs/heads/"+ref]
		_, tagCommit, isTag := pick("refs/tags/" + ref)
		switch {
		case isBranch && isTag:
			return "", "", fmt.Errorf("ref %q is both a branch and a tag: set ref to refs/heads/%s or refs/tags/%s", ref, ref, ref)
		case isBranch:
			return "refs/heads/" + ref, branch, nil
		case isTag:
			return "refs/tags/" + ref, tagCommit, nil
		}
	}
	return "", "", fmt.Errorf("ref %q is not a branch or tag of the repository", ref)
}

// access adds what to check to an error from the remote.
func access(err error, repo string) error {
	if !errors.Is(err, ErrGit) {
		return err
	}
	repo = Redact(repo)
	return fmt.Errorf("%w\nstele needs read access to %s: check that git ls-remote %s works here", err, repo, repo)
}

// cmd returns git, found once, and the URL it fetches repo from.
func (f *Fetcher) cmd(repo string) (Cmd, string, error) {
	if f.git == nil {
		bin, err := Tool("git", f.Root)
		if err != nil {
			return Cmd{}, "", fmt.Errorf("the git source needs git: %w", err)
		}
		g := New(bin, f.Root)
		g.NetTimeout = fetchNetTimeout
		f.git = &g
	}
	g, url := *f.git, repo
	if local, ok := f.Remotes[repo]; ok {
		g.AllowFile, url = true, local
	}
	return g, url, nil
}

// dir is repo's bare clone in the cache.
func (f *Fetcher) dir(repo string) (string, error) {
	base, err := f.CacheDir()
	if err != nil {
		return "", fmt.Errorf("no user cache directory: %w", err)
	}
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(base, "stele", "sources", hex.EncodeToString(sum[:])), nil
}

// lock takes the lock of repo's cache.
func (f *Fetcher) lock(repo string) (string, func(), error) {
	dir, err := f.dir(repo)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", nil, err
	}
	unlock, err := safefile.Lock(dir+".lock", cacheWait, cacheStale)
	if errors.Is(err, fs.ErrExist) {
		return "", nil, fmt.Errorf("another stele is fetching %s; try again when it ends", repo)
	}
	if err != nil {
		return "", nil, err
	}
	return dir, unlock, nil
}

// bareRun runs git on the bare clone at dir, naming it with --git-dir so git
// never looks for a repository above it.
func bareRun(ctx context.Context, g Cmd, dir string, args ...string) (string, error) {
	full := append(append(append([]string{}, fetchFlags...), "--git-dir="+dir), args...)
	return g.Run(ctx, dir, nil, nil, full...)
}

func bareRepo(ctx context.Context, g Cmd, dir string) bool {
	out, err := bareRun(ctx, g, dir, "rev-parse", "--is-bare-repository")
	return err == nil && strings.TrimSpace(out) == "true"
}

// localRef is where the clone keeps what ref named at the last fetch: per
// ref as stele.json spells it, "" being the default branch.
func localRef(ref string) string {
	if ref == "" {
		return "refs/stele/default"
	}
	return "refs/stele/named/" + ref
}

// onRef reports whether the clone at dir holds commit, and holds it on ref
// as last fetched: the ref's commit or one before it.
func onRef(ctx context.Context, g Cmd, dir, ref, commit string) bool {
	if !has(ctx, g, dir, commit) {
		return false
	}
	_, err := bareRun(ctx, g, dir, "merge-base", "--is-ancestor", commit, localRef(ref))
	return err == nil
}

// has reports whether the clone at dir holds commit.
func has(ctx context.Context, g Cmd, dir, commit string) bool {
	if !bareRepo(ctx, g, dir) {
		return false
	}
	_, err := bareRun(ctx, g, dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
