package gitexec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRepo = "git@example.com:acme/guidelines.git"

// guidelines makes a bare repository with stele.md on main, a branch dev,
// an annotated tag v1 and a light tag v0, and returns it with a work clone.
func guidelines(t *testing.T) (bare, work string) {
	t.Helper()
	testGit(t)
	bare = filepath.Join(t.TempDir(), "g.git")
	gitT(t, filepath.Dir(bare), "init", "--bare", "--quiet", "--initial-branch=main", bare)
	work = t.TempDir()
	gitT(t, work, "init", "--quiet", "--initial-branch=main")
	writeFile(t, filepath.Join(work, "stele.md"), "Rule one.\n")
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "--quiet", "-m", "one")
	gitT(t, work, "tag", "v0")
	gitT(t, work, "tag", "-a", "-m", "v1", "v1")
	gitT(t, work, "branch", "dev")
	gitT(t, work, "push", "--quiet", "--tags", bare, "main", "dev")
	return bare, work
}

// push commits content as stele.md in work and pushes main.
func push(t *testing.T, work, bare, content string) string {
	t.Helper()
	writeFile(t, filepath.Join(work, "stele.md"), content)
	gitT(t, work, "commit", "--quiet", "-am", "next")
	gitT(t, work, "push", "--quiet", "--force", bare, "main")
	return gitT(t, work, "rev-parse", "HEAD")
}

func fetcher(t *testing.T, bare string) *Fetcher {
	t.Helper()
	cache := t.TempDir()
	return &Fetcher{
		Root:     t.TempDir(),
		CacheDir: func() (string, error) { return cache, nil },
		Remotes:  map[string]string{testRepo: bare},
	}
}

func TestFetcherResolve(t *testing.T) {
	bare, work := guidelines(t)
	f := fetcher(t, bare)
	ctx := context.Background()
	first := gitT(t, work, "rev-parse", "HEAD")
	for _, ref := range []string{"", "main", "dev", "v0", "v1", "refs/heads/main", "refs/tags/v1"} {
		got, err := f.Resolve(ctx, testRepo, ref)
		if err != nil || got != first {
			t.Errorf("Resolve(%q) = %q, %v; want %s", ref, got, err, first)
		}
	}
	if _, err := f.Resolve(ctx, testRepo, "nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("Resolve of a missing ref = %v", err)
	}
	second := push(t, work, bare, "Rule two.\n")
	if got, err := f.Resolve(ctx, testRepo, "main"); err != nil || got != second {
		t.Errorf("Resolve after a push = %q, %v; want %s", got, err, second)
	}
}

func TestFetcherRefusesABranchThatIsATag(t *testing.T) {
	bare, work := guidelines(t)
	gitT(t, work, "branch", "v0")
	gitT(t, work, "push", "--quiet", bare, "refs/heads/v0")
	_, err := fetcher(t, bare).Resolve(context.Background(), testRepo, "v0")
	if err == nil || !strings.Contains(err.Error(), "refs/heads/v0") {
		t.Errorf("Resolve of a branch and tag = %v, want it to ask for the full name", err)
	}
}

func TestFetcherFile(t *testing.T) {
	bare, work := guidelines(t)
	f := fetcher(t, bare)
	ctx := context.Background()
	commit, err := f.Resolve(ctx, testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.File(ctx, testRepo, commit, "stele.md", 1<<20)
	if err != nil || string(data) != "Rule one.\n" {
		t.Errorf("File = %q, %v", data, err)
	}
	if _, err := f.File(ctx, testRepo, commit, "other/stele.md", 1<<20); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("File of a missing path = %v, want ErrNotExist", err)
	}
	if _, err := f.File(ctx, testRepo, commit, "stele.md", 4); err == nil || !strings.Contains(err.Error(), "over 4 bytes") {
		t.Errorf("File over the cap = %v", err)
	}

	// A link and a submodule are refused: git would write neither as a file.
	blob := gitT(t, work, "hash-object", "-w", "--stdin", "--path=x")
	gitT(t, work, "update-index", "--add", "--cacheinfo", "120000,"+blob+",link.md")
	gitT(t, work, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sub.md")
	gitT(t, work, "commit", "--quiet", "-m", "odd")
	gitT(t, work, "push", "--quiet", bare, "main")
	odd, err := f.Resolve(ctx, testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link.md", "sub.md"} {
		if _, err := f.File(ctx, testRepo, odd, p, 1<<20); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("File(%s) = %v, want a refusal", p, err)
		}
	}
}

// A commit in the cache is read with no network call.
func TestFetcherEnsureOffline(t *testing.T) {
	bare, _ := guidelines(t)
	f := fetcher(t, bare)
	ctx := context.Background()
	commit, err := f.Resolve(ctx, testRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	f.Remotes[testRepo] = filepath.Join(t.TempDir(), "gone.git")
	if err := f.Ensure(ctx, testRepo, "main", commit); err != nil {
		t.Errorf("Ensure of a cached commit with the remote gone = %v", err)
	}
	if _, err := f.File(ctx, testRepo, commit, "stele.md", 1<<20); err != nil {
		t.Errorf("File offline = %v", err)
	}
}

// On a machine with no cache, Ensure fetches the pinned commit.
func TestFetcherEnsureFetches(t *testing.T) {
	bare, work := guidelines(t)
	first := gitT(t, work, "rev-parse", "HEAD")
	push(t, work, bare, "Rule two.\n")
	f := fetcher(t, bare)
	if err := f.Ensure(context.Background(), testRepo, "main", first); err != nil {
		t.Fatalf("Ensure of an older commit on main = %v", err)
	}
	if data, err := f.File(context.Background(), testRepo, first, "stele.md", 1<<20); err != nil || string(data) != "Rule one.\n" {
		t.Errorf("File at the pin = %q, %v", data, err)
	}
}

// A pin is good only on its ref: a commit the remote has elsewhere, such as
// a fork's pull request GitHub serves from the parent, is refused, even
// when it is in the cache.
func TestFetcherEnsureRefusesACommitOffTheRef(t *testing.T) {
	bare, work := guidelines(t)
	gitT(t, work, "checkout", "--quiet", "-b", "weak")
	writeFile(t, filepath.Join(work, "stele.md"), "Anything goes.\n")
	gitT(t, work, "commit", "--quiet", "-am", "weaker rules")
	gitT(t, work, "push", "--quiet", bare, "weak")
	weak := gitT(t, work, "rev-parse", "HEAD")
	f := fetcher(t, bare)
	ctx := context.Background()
	if got, err := f.Resolve(ctx, testRepo, "weak"); err != nil || got != weak {
		t.Fatalf("Resolve(weak) = %q, %v", got, err)
	}
	err := f.Ensure(ctx, testRepo, "main", weak)
	if !errors.Is(err, ErrPinGone) {
		t.Errorf("Ensure of a commit off main = %v, want ErrPinGone", err)
	}
	if err := f.Ensure(ctx, testRepo, "weak", weak); err != nil {
		t.Errorf("Ensure on its own ref = %v", err)
	}
}

func TestFetcherEnsureMissingCommit(t *testing.T) {
	bare, _ := guidelines(t)
	err := fetcher(t, bare).Ensure(context.Background(), testRepo, "main", strings.Repeat("1", 40))
	if err == nil || !strings.Contains(err.Error(), "stele compile --update") {
		t.Errorf("Ensure of a commit the remote lacks = %v, want it to say stele compile --update", err)
	}
}

// What stele.json supplies is refused before git sees it.
func TestFetcherRefusesOptions(t *testing.T) {
	f := &Fetcher{Root: t.TempDir(), CacheDir: func() (string, error) { return t.TempDir(), nil }}
	ctx := context.Background()
	for _, ref := range []string{"-x", "--upload-pack=calc", "a b", "a\nb"} {
		if _, err := f.Resolve(ctx, testRepo, ref); err == nil || !strings.Contains(err.Error(), "ref") {
			t.Errorf("Resolve(ref %q) = %v, want a refusal", ref, err)
		}
	}
	for _, repo := range []string{"ext::sh -c x", "file:///x", "/abs", "ssh://-oProxyCommand=x@h/p", "--upload-pack=x"} {
		if _, err := f.Remote(repo); err == nil {
			t.Errorf("Remote(%q) accepted", repo)
		}
	}
	if _, err := f.File(ctx, testRepo, "--output=x", "stele.md", 10); err == nil {
		t.Error("File accepted a commit that is an option")
	}
}

func TestFetcherRemoteLabel(t *testing.T) {
	f := &Fetcher{}
	for repo, want := range map[string]string{
		"git@github.com:acme/guidelines.git": "github.com/acme/guidelines",
		"https://gitlab.com/a/b/c.git":       "gitlab.com/a/b/c",
		"ssh://git@host.example:2222/x/y":    "host.example/x/y",
	} {
		if got, err := f.Remote(repo); err != nil || got != want {
			t.Errorf("Remote(%q) = %q, %v; want %q", repo, got, err, want)
		}
	}
}

func TestFetcherCacheOutsideProject(t *testing.T) {
	bare, _ := guidelines(t)
	f := fetcher(t, bare)
	if _, err := f.Resolve(context.Background(), testRepo, ""); err != nil {
		t.Fatal(err)
	}
	cache, _ := f.CacheDir()
	entries, err := os.ReadDir(filepath.Join(cache, "stele", "sources"))
	if err != nil || len(entries) == 0 {
		t.Errorf("cache under %s = %v, %v", cache, entries, err)
	}
	if entries, _ := os.ReadDir(f.Root); len(entries) != 0 {
		t.Errorf("the project holds %v, want nothing", entries)
	}
}

func TestRedact(t *testing.T) {
	in := "fatal: unable to access 'https://x-access-token:ghp_secret@github.com/a/b.git/': 403"
	got := Message(in)
	if strings.Contains(got, "ghp_secret") || !strings.Contains(got, "https://***@github.com/a/b.git/") {
		t.Errorf("Message = %q, want the credentials hidden", got)
	}
}
