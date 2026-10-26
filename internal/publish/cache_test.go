package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/safefile"
)

// setCacheDir points the cache at a temporary directory for one test.
func setCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := cacheDir
	cacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheDir = old })
	return dir
}

// newRemote makes a bare repository holding files on main, and returns its
// path. An empty files map makes an empty repository.
func newRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	testGit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitT(t, filepath.Dir(remote), "init", "--bare", "--quiet", "--initial-branch=main", remote)
	if len(files) == 0 {
		return remote
	}
	work := t.TempDir()
	gitT(t, work, "init", "--quiet", "--initial-branch=main")
	for name, content := range files {
		writeFile(t, filepath.Join(work, filepath.FromSlash(name)), content)
	}
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "--quiet", "-m", "first")
	gitT(t, work, "push", "--quiet", remote, "main")
	return remote
}

func TestCachePath(t *testing.T) {
	dir := setCacheDir(t)
	got, err := cachePath("git@github.com:me/g.git")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("git@github.com:me/g.git"))
	want := filepath.Join(dir, "stele", "publish", hex.EncodeToString(sum[:]))
	if got != want {
		t.Errorf("cachePath = %s, want %s", got, want)
	}
}

func TestLsRemote(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	remote := newRemote(t, map[string]string{"README.md": "hi\n"})
	setLocalRemote(t, remote)
	branch := "refs/heads/stele/publish/x.md"
	s, err := lsRemote(ctx, g, t.TempDir(), remote, branch)
	if err != nil {
		t.Fatal(err)
	}
	if s.Base != "main" || s.Tip != "" {
		t.Errorf("state = %+v, want main and no tip", s)
	}
	gitT(t, remote, "update-ref", branch, "refs/heads/main")
	s, err = lsRemote(ctx, g, t.TempDir(), remote, branch)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tip != gitT(t, remote, "rev-parse", "main") {
		t.Errorf("tip = %q", s.Tip)
	}
}

func TestLsRemoteEmpty(t *testing.T) {
	g := testGit(t)
	remote := newRemote(t, nil)
	setLocalRemote(t, remote)
	if _, err := lsRemote(context.Background(), g, t.TempDir(), remote, "refs/heads/stele/publish/x.md"); !errors.Is(err, ErrEmptyRepo) {
		t.Errorf("err = %v, want ErrEmptyRepo", err)
	}
}

func TestSyncCache(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	setCacheDir(t)
	remote := newRemote(t, map[string]string{"README.md": "hi\n"})
	setLocalRemote(t, remote)
	dir, err := cachePath("https://example.invalid/me/rules.git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	branch := "refs/heads/stele/publish/x.md"
	s := remoteState{Base: "main"}
	if err := syncCache(ctx, g, dir, remote, s, branch); err != nil {
		t.Fatal(err)
	}
	if out, _ := bare(ctx, g, dir, nil, nil, "rev-parse", "--is-bare-repository"); strings.TrimSpace(out) != "true" {
		t.Errorf("cache is not bare: %q", out)
	}
	// A second sync fetches what is new.
	work := t.TempDir()
	gitT(t, work, "clone", "--quiet", remote, ".")
	writeFile(t, filepath.Join(work, "new.md"), "new\n")
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "--quiet", "-m", "second")
	gitT(t, work, "push", "--quiet", "origin", "main")
	if err := syncCache(ctx, g, dir, remote, s, branch); err != nil {
		t.Fatal(err)
	}
	got, _ := bare(ctx, g, dir, nil, nil, "rev-parse", "refs/heads/main")
	if strings.TrimSpace(got) != gitT(t, remote, "rev-parse", "main") {
		t.Errorf("cache main = %q, want the remote's", got)
	}
	// Nothing is checked out.
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && (d.Name() == "README.md" || d.Name() == "new.md") {
			t.Errorf("checked out file %s", p)
		}
		return nil
	})
}

func TestSyncCacheFetchesOnlyWhatItNeeds(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	setCacheDir(t)
	remote := newRemote(t, map[string]string{"README.md": "hi\n"})
	gitT(t, remote, "update-ref", "refs/heads/other", "refs/heads/main")
	gitT(t, remote, "tag", "v1", "refs/heads/main")
	setLocalRemote(t, remote)
	dir, _ := cachePath("https://example.invalid/me/rules.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syncCache(ctx, g, dir, remote, remoteState{Base: "main"}, "refs/heads/stele/publish/x.md"); err != nil {
		t.Fatal(err)
	}
	refs, _ := bare(ctx, g, dir, nil, nil, "for-each-ref", "--format=%(refname)")
	if strings.TrimSpace(refs) != "refs/heads/main" {
		t.Errorf("cache refs =\n%s\nwant main alone", refs)
	}
}

func TestTimeoutFor(t *testing.T) {
	for _, args := range [][]string{
		{"clone", "--bare"}, {"--git-dir=x", "fetch"}, {"--git-dir=x", "push"}, {"ls-remote", "--symref"},
	} {
		if got := timeoutFor(args); got != netTimeout {
			t.Errorf("timeoutFor(%q) = %v, want %v", args, got, netTimeout)
		}
	}
	for _, args := range [][]string{{"--git-dir=x", "write-tree"}, {"rev-parse", "HEAD"}, {"credential", "fill"}} {
		if got := timeoutFor(args); got != gitTimeout {
			t.Errorf("timeoutFor(%q) = %v, want %v", args, got, gitTimeout)
		}
	}
}

func TestSyncCacheReplacesGarbage(t *testing.T) {
	g := testGit(t)
	ctx := context.Background()
	setCacheDir(t)
	remote := newRemote(t, map[string]string{"README.md": "hi\n"})
	setLocalRemote(t, remote)
	dir, _ := cachePath("https://example.invalid/me/rules.git")
	writeFile(t, filepath.Join(dir, "junk"), "junk")
	if err := syncCache(ctx, g, dir, remote, remoteState{Base: "main"}, "refs/heads/stele/publish/x.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the garbage is still there")
	}
}

func TestLockCacheBusy(t *testing.T) {
	setCacheDir(t)
	old := lockWait
	lockWait = 100 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	dir, _ := cachePath("https://example.invalid/me/rules.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := safefile.Lock(dir+".lock", time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lockCache(dir); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}
