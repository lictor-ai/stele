package publish

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrForeignBranch is a publish branch whose tip stele publish did not make
// for this project: a person's commit, or another project that chose the
// same path. stele never overwrites it.
var ErrForeignBranch = errors.New("holds commits stele publish did not make for this project; it is left alone")

// BranchPrefix starts the name of every publish branch.
const BranchPrefix = "stele/publish/"

// Result is what Run did.
type Result struct {
	Repo   string // "repo" from stele.json
	Path   string // where stele.md went in it
	Base   string // the repository's default branch
	Branch string // the publish branch, stele/publish/<path>

	// Unchanged is true when the default branch already holds this
	// stele.md: nothing was pushed and no merge request was looked for.
	Unchanged bool
	// Pushed is false when the publish branch already held this stele.md.
	Pushed bool

	MR       string // the merge request's link, from the host
	MRAction string // "opened", "found" or "reopened"
	Page     string // when there is no MR: the host's page to open one, or "" for another host
	Why      string // when there is no MR on github.com or gitlab.com: why not
}

// beforePush runs just before the push. Only tests set it.
var beforePush func()

// Run publishes the committed stele.md of the project at root to the
// repository named in its stele.json (D64).
func Run(ctx context.Context, root string) (Result, error) {
	cfg, err := LoadConfig(root)
	if err != nil {
		return Result{}, err
	}
	gitBin, err := resolveTool("git", root)
	if err != nil {
		return Result{}, err
	}
	g := gitCmd{bin: gitBin}
	c, err := readCommitted(ctx, g, root)
	if err != nil {
		return Result{}, err
	}
	res := Result{Repo: cfg.Repo, Path: cfg.Path, Branch: BranchPrefix + cfg.Path}
	branchRef := "refs/heads/" + res.Branch

	url := cfg.Repo
	if localRemote != "" {
		url = localRemote
	}
	dir, err := cachePath(cfg.Repo)
	if err != nil {
		return Result{}, err
	}
	unlock, err := lockCache(dir)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	parent := filepath.Dir(dir)
	if _, err := g.run(ctx, parent, nil, nil, "check-ref-format", branchRef); err != nil {
		return Result{}, fmt.Errorf("%w: path %q does not make a valid branch name", ErrBadConfig, cfg.Path)
	}

	s, err := lsRemote(ctx, g, parent, url, branchRef)
	if err != nil {
		return Result{}, err
	}
	res.Base = s.Base
	if err := syncCache(ctx, g, dir, url, s, branchRef); err != nil {
		return Result{}, err
	}
	blob, err := hashBlob(ctx, g, dir, c.Blob)
	if err != nil {
		return Result{}, err
	}
	baseRef := "refs/heads/" + s.Base
	if blobAt(ctx, g, dir, baseRef, cfg.Path) == blob {
		res.Unchanged = true
		return res, nil
	}

	push := true
	if s.Tip != "" {
		ours, err := isOurs(ctx, g, dir, s.Tip, cfg.Path, c.Project)
		if err != nil {
			return Result{}, err
		}
		if !ours {
			return Result{}, fmt.Errorf("branch %s in %s %w", res.Branch, cfg.Repo, ErrForeignBranch)
		}
		// Rebuild when the branch holds an older stele.md, or sits on an
		// older base: its merge request would otherwise bring that base
		// back.
		push = blobAt(ctx, g, dir, s.Tip, cfg.Path) != blob || revParse(ctx, g, dir, s.Tip+"^") != revParse(ctx, g, dir, baseRef)
	}
	if push {
		commit, err := buildCommit(ctx, g, dir, baseRef, cfg.Path, blob, c)
		if err != nil {
			return Result{}, err
		}
		if beforePush != nil {
			beforePush()
		}
		lease := "--force-with-lease=" + branchRef + ":" + s.Tip
		if _, err := bare(ctx, g, dir, nil, nil, "push", "--porcelain", "--no-recurse-submodules", lease, "--", url, commit+":"+branchRef); err != nil {
			return Result{}, err
		}
		res.Pushed = true
	}

	mergeRequest(ctx, g, root, parent, parseRemote(cfg.Repo), c, &res)
	return res, nil
}

// blobAt returns the blob at path in rev, or "" if there is none.
func blobAt(ctx context.Context, g gitCmd, dir, rev, path string) string {
	return revParse(ctx, g, dir, rev+":"+path)
}

// revParse returns the object rev names, or "" if it names none.
func revParse(ctx context.Context, g gitCmd, dir, rev string) string {
	out, err := bare(ctx, g, dir, nil, nil, "rev-parse", "--verify", "-q", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
