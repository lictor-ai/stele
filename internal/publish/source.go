package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lictor-ai/stele/internal/lock"
)

var (
	// ErrNotRepo is a project that is not in a git repository.
	ErrNotRepo = errors.New("the project is not in a git repository; stele publish sends the committed stele.md")
	// ErrUntracked is a stele.md that is not in the project's HEAD commit.
	ErrUntracked = errors.New("stele.md is not committed")
	// ErrEdited is a stele.md changed since the HEAD commit.
	ErrEdited = errors.New("stele.md has changes that are not committed; commit it first")
	// ErrNoProject is a project with no project id in stele.lock (D51).
	ErrNoProject = errors.New("stele.lock has no project id; run stele compile first")
)

// rulesFile is the file stele publish sends.
const rulesFile = "stele.md"

// maxRules caps the stele.md stele publishes.
const maxRules = 16 << 20

// committed is what stele publish takes from the project.
type committed struct {
	Blob    []byte // stele.md as committed at HEAD
	Head    string // the project's HEAD commit
	Name    string // the developer's git identity, as the project sees it
	Email   string
	Project string // the project id from stele.lock (D51)
}

// readCommitted reads stele.md from the project's HEAD commit with git,
// never from the working file, so an uncommitted edit or a line-ending
// conversion cannot change what is sent. It copies the file and never parses
// it: the one read of a rule file outside a source plugin (D40, D64).
func readCommitted(ctx context.Context, g gitCmd, root string) (committed, error) {
	var c committed
	top, err := g.run(ctx, root, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return c, ErrNotRepo
	}
	gitDir, err := g.run(ctx, root, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return c, ErrNotRepo
	}
	if err := checkGitDir(strings.TrimSpace(top), strings.TrimSpace(gitDir)); err != nil {
		return c, err
	}
	if _, err := g.run(ctx, root, nil, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err != nil {
		return c, fmt.Errorf("%w: the repository has no commit yet", ErrUntracked)
	}
	prefix, err := g.run(ctx, root, nil, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return c, err
	}
	path := strings.TrimSpace(prefix) + rulesFile
	tree, err := g.run(ctx, root, nil, nil, "ls-tree", "HEAD", "--", rulesFile)
	if err != nil {
		return c, err
	}
	mode, _, _ := strings.Cut(strings.TrimSpace(tree), " ")
	switch mode {
	case "":
		return c, fmt.Errorf("%w: it is not in the HEAD commit", ErrUntracked)
	case "100644", "100755":
	default:
		return c, fmt.Errorf("%w: it is committed as something other than a plain file (mode %s)", ErrUntracked, mode)
	}
	if _, err := g.run(ctx, root, nil, nil, "diff", "--quiet", "HEAD", "--", rulesFile); err != nil {
		if exitCode(err) == 1 {
			return c, ErrEdited
		}
		return c, err
	}
	size, err := g.run(ctx, root, nil, nil, "cat-file", "-s", "HEAD:"+path)
	if err != nil {
		return c, err
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil || n > maxRules {
		return c, fmt.Errorf("stele.md is over %d bytes", maxRules)
	}
	blob, err := g.run(ctx, root, nil, nil, "cat-file", "blob", "HEAD:"+path)
	if err != nil {
		return c, err
	}
	c.Blob = []byte(blob)
	head, err := g.run(ctx, root, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return c, err
	}
	c.Head = strings.TrimSpace(head)
	ident, err := g.run(ctx, root, nil, nil, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return c, err
	}
	if c.Name, c.Email, err = parseIdent(ident); err != nil {
		return c, err
	}
	f, err := lock.Load(root)
	if err != nil {
		return c, err
	}
	if f.Project == "" {
		return c, ErrNoProject
	}
	c.Project = f.Project
	return c, nil
}

// checkGitDir accepts the repository git found only when it is the one a
// checkout has: <top>/.git, or the directory a worktree's or submodule's .git
// file names. Anything else, such as a project laid out as a bare repository
// whose config points its worktree back at itself, is refused before git
// reads a file through it.
func checkGitDir(top, gitDir string) error {
	dotGit := filepath.Join(filepath.FromSlash(top), ".git")
	want := dotGit
	info, err := os.Lstat(dotGit)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %s has no .git", ErrNotRepo, top)
	case info.Mode().IsRegular():
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return err
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return fmt.Errorf("%w: %s is not a gitdir file", ErrNotRepo, dotGit)
		}
		want = filepath.FromSlash(strings.TrimSpace(target))
		if !filepath.IsAbs(want) {
			want = filepath.Join(filepath.FromSlash(top), want)
		}
	case !info.IsDir():
		return fmt.Errorf("%w: %s is neither a directory nor a gitdir file", ErrNotRepo, dotGit)
	}
	if !samePath(filepath.FromSlash(gitDir), want) {
		return fmt.Errorf("%w: git found the repository %s, not %s", ErrNotRepo, gitDir, want)
	}
	return nil
}

// samePath compares two paths after resolving links, without case on
// Windows.
func samePath(a, b string) bool {
	if ea, err := filepath.EvalSymlinks(a); err == nil {
		a = ea
	}
	if eb, err := filepath.EvalSymlinks(b); err == nil {
		b = eb
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// parseIdent splits git's "Name <email> time zone" into name and email.
func parseIdent(ident string) (name, email string, err error) {
	lt := strings.LastIndex(ident, "<")
	gt := strings.LastIndex(ident, ">")
	if lt < 0 || gt < lt {
		return "", "", fmt.Errorf("cannot read the git identity %q", strings.TrimSpace(ident))
	}
	return strings.TrimSpace(ident[:lt]), ident[lt+1 : gt], nil
}
