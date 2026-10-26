package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/safefile"
)

var (
	// ErrEmptyRepo is a repository with no default branch to commit on.
	ErrEmptyRepo = errors.New("the repository has no default branch; push a first commit to it")
	// ErrBusy is the cache of this repository in use by another stele publish.
	ErrBusy = errors.New("another stele publish is using this repository's cache; try again when it ends")
)

// cacheDir is the user's cache directory. Tests replace it.
var cacheDir = os.UserCacheDir

// lockWait is how long stele publish waits for another one to finish with
// the cache. Tests shorten it.
var lockWait = 5 * time.Second

// lockStale is the age at which a cache lock was left by a crash. It is
// longer than the longest run: four git calls that talk to the remote, each
// up to netTimeout, and the local ones.
const lockStale = 3 * time.Hour

// cacheRoot is the directory that holds every publish cache.
func cacheRoot() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", fmt.Errorf("no user cache directory: %w", err)
	}
	return filepath.Join(dir, "stele", "publish"), nil
}

// cachePath is the bare clone of repo: one per repository, named by the
// SHA-256 of the URL.
func cachePath(repo string) (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(root, hex.EncodeToString(sum[:])), nil
}

// lockCache takes the lock of the cache at dir.
func lockCache(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	unlock, err = safefile.Lock(dir+".lock", lockWait, lockStale)
	if errors.Is(err, fs.ErrExist) {
		return nil, ErrBusy
	}
	return unlock, err
}

// remoteState is what ls-remote says of the repository.
type remoteState struct {
	Base string // the default branch
	Tip  string // the publish branch's commit, or "" if it does not exist
}

// lsRemote reads the repository's default branch and the tip of branchRef
// before anything is cloned, so an empty repository leaves no cache behind.
func lsRemote(ctx context.Context, g gitCmd, dir, url, branchRef string) (remoteState, error) {
	out, err := g.run(ctx, dir, nil, nil, "ls-remote", "--symref", "--", url, "HEAD", branchRef)
	if err != nil {
		return remoteState{}, err
	}
	var s remoteState
	for _, line := range strings.Split(out, "\n") {
		left, right, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		switch {
		case right == "HEAD" && strings.HasPrefix(left, "ref: refs/heads/"):
			s.Base = strings.TrimPrefix(left, "ref: refs/heads/")
		case right == branchRef:
			s.Tip = left
		}
	}
	if s.Base == "" {
		return remoteState{}, ErrEmptyRepo
	}
	return s, nil
}

// bare runs git on the bare clone at dir, naming it with --git-dir so git
// never looks for a repository above it.
func bare(ctx context.Context, g gitCmd, dir string, env []string, stdin []byte, args ...string) (string, error) {
	return g.run(ctx, dir, env, stdin, append([]string{"--git-dir=" + dir}, args...)...)
}

// syncCache makes dir a bare clone of url holding base, and branchRef when
// the remote has it. Nothing is ever checked out, so no file of the
// repository is written as a file and none of its hooks runs.
func syncCache(ctx context.Context, g gitCmd, dir, url string, s remoteState, branchRef string) error {
	if out, err := bare(ctx, g, dir, nil, nil, "rev-parse", "--is-bare-repository"); err != nil || strings.TrimSpace(out) != "true" {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		// Only the default branch: the publish branch comes with the fetch
		// below. clone, unlike init, takes the remote's object format.
		if _, err := g.run(ctx, filepath.Dir(dir), nil, nil, "clone", "--bare", "--single-branch", "--branch", s.Base, "--no-tags", "--quiet", "--", url, dir); err != nil {
			return err
		}
	}
	baseRef := "refs/heads/" + s.Base
	specs := []string{"+" + baseRef + ":" + baseRef}
	if s.Tip != "" {
		specs = append(specs, "+"+branchRef+":"+branchRef)
	} else if _, err := bare(ctx, g, dir, nil, nil, "rev-parse", "--verify", "-q", branchRef); err == nil {
		if _, err := bare(ctx, g, dir, nil, nil, "update-ref", "-d", branchRef); err != nil {
			return err
		}
	}
	args := append([]string{"fetch", "--no-tags", "--no-recurse-submodules", "--quiet", "--", url}, specs...)
	_, err := bare(ctx, g, dir, nil, nil, args...)
	return err
}
