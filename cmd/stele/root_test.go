package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// gitDir makes d a directory git takes for a repository: HEAD, objects/
// and refs/.
func gitDir(t *testing.T, d string) {
	t.Helper()
	for _, sub := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitlink makes rel, under root, a checkout as a submodule or worktree is:
// a .git file whose "gitdir: <path>" leads to a repository elsewhere.
func gitlink(t *testing.T, root, rel string) {
	t.Helper()
	d := filepath.Join(t.TempDir(), "modules", "x")
	gitDir(t, d)
	writeAt(t, root, rel+"/.git", "gitdir: "+filepath.ToSlash(d)+"\n")
}

func TestFindRoot(t *testing.T) {
	// repo/.git, repo/sub/stele.md, repo/sub/deep/, repo/other/
	repo := t.TempDir()
	gitDir(t, filepath.Join(repo, ".git"))
	for _, d := range []string{"sub/deep", "other"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "stele.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, from, want string
	}{
		{name: "stele.md wins over .git", from: "sub/deep", want: "sub"},
		{name: "stele.md in the directory itself", from: "sub", want: "sub"},
		{name: "falls back to .git", from: "other", want: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findRoot(filepath.Join(repo, tt.from))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(repo, tt.want); got != want {
				t.Errorf("findRoot(%s) = %s, want %s", tt.from, got, want)
			}
		})
	}
}

// A .git git recognises, a directory or the file a worktree or submodule
// has, ends the search: rules never cross into another checkout (#43).
func TestFindRootStopsAtGit(t *testing.T) {
	// repo/.git/, repo/stele.md, repo/wt/.git (file), repo/wt/deep/,
	// repo/nested/.git/, repo/sub/stele.md, repo/sub/deep/,
	// repo/linked/.git (file, relative, to a worktree's gitdir)
	repo := t.TempDir()
	gitDir(t, filepath.Join(repo, ".git"))
	gitDir(t, filepath.Join(repo, "nested", ".git"))
	for _, d := range []string{"wt/deep", "sub/deep", "linked/deep"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitlink(t, repo, "wt")
	// A worktree's gitdir has HEAD and a commondir, no objects/ or refs/.
	writeAt(t, repo, ".git/worktrees/linked/HEAD", "ref: refs/heads/b\n")
	writeAt(t, repo, ".git/worktrees/linked/commondir", "../..\n")
	writeAt(t, repo, "linked/.git", "gitdir: ../.git/worktrees/linked\n")
	writeAt(t, repo, "stele.md", "")
	writeAt(t, repo, "sub/stele.md", "")
	for from, want := range map[string]string{"wt/deep": "wt", "wt": "wt", "nested": "nested", "sub/deep": "sub", ".": ".", "linked/deep": "linked"} {
		got, err := findRoot(filepath.Join(repo, from))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(repo, want); got != want {
			t.Errorf("findRoot(%s) = %s, want %s", from, got, want)
		}
	}
}

// A .git git does not recognise ends nothing: git lists the files beside it
// as the outer repository's, so the outer rules reach them too.
func TestFindRootIgnoresGitGitDoesNotRecognise(t *testing.T) {
	repo := t.TempDir()
	gitDir(t, filepath.Join(repo, ".git"))
	writeAt(t, repo, "stele.md", "")
	for dir, files := range map[string]map[string]string{
		"empty":       {".git/x": ""},
		"nohead":      {".git/objects/x": "", ".git/refs/x": ""},
		"noobjects":   {".git/HEAD": "ref: refs/heads/main\n", ".git/refs/x": ""},
		"emptyhead":   {".git/HEAD": "", ".git/objects/x": "", ".git/refs/x": ""},
		"junkhead":    {".git/HEAD": "x\n", ".git/objects/x": "", ".git/refs/x": ""},
		"badref":      {".git/HEAD": "ref: heads/main\n", ".git/objects/x": "", ".git/refs/x": ""},
		"shortid":     {".git/HEAD": "abc123\n", ".git/objects/x": "", ".git/refs/x": ""},
		"emptycommon": {".git": "gitdir: wt2\n", "wt2/HEAD": "ref: refs/heads/main\n", "wt2/commondir": ""},
		"uncgitdir":   {".git": "gitdir: //host/share/x\n"},
		"headdir":     {".git/HEAD/x": "", ".git/objects/x": "", ".git/refs/x": ""},
		"plainfile":   {".git": "not a gitlink\n"},
		"missing":     {".git": "gitdir: ../nowhere\n"},
		"emptylink":   {".git": "gitdir: \n"},
		"tojunk":      {".git": "gitdir: ../empty/.git\n"},
		"badcommon":   {".git": "gitdir: wt\n", "wt/HEAD": "ref: refs/heads/main\n", "wt/commondir": "../nowhere\n"},
	} {
		for name, data := range files {
			writeAt(t, repo, dir+"/"+name, data)
		}
		if holdsGit(filepath.Join(repo, dir)) {
			t.Errorf("holdsGit(%s) = true, want false", dir)
		}
		got, err := findRoot(filepath.Join(repo, dir))
		if err != nil {
			t.Fatal(err)
		}
		if got != repo {
			t.Errorf("findRoot(%s) = %s, want the outer root %s", dir, got, repo)
		}
	}
}

// A .git that is a dangling link is no repository either: the hook's root
// search and check's and init's holdsGit agree on it.
func TestFindRootDanglingGitLink(t *testing.T) {
	repo := t.TempDir()
	gitDir(t, filepath.Join(repo, ".git"))
	if err := os.WriteFile(filepath.Join(repo, "stele.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	danglingLink(t, filepath.Join(sub, ".git"))

	got, err := findRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != repo || holdsGit(sub) {
		t.Errorf("findRoot = %s, holdsGit = %v; want %s and false", got, holdsGit(sub), repo)
	}
}

// danglingLink makes link a symlink, or on Windows without the privilege a
// junction, to a directory that no longer exists.
func danglingLink(t *testing.T, link string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS != "windows" {
			t.Skipf("cannot make a symlink: %v", err)
		}
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Skipf("cannot make a symlink or a junction: %v\n%s", err, out)
		}
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
}

// worktree adds a worktree of main at .claude/worktrees/<name> on a new
// branch from HEAD, where Claude Code puts an agent's.
func worktree(t *testing.T, main, name string) string {
	t.Helper()
	dir := filepath.Join(main, ".claude", "worktrees", name)
	gitT(t, main, "worktree", "add", "-q", "-b", name, dir)
	return dir
}

func TestFindRootInWorktree(t *testing.T) {
	main := gitProject(t, "")
	wt := worktree(t, main, "wt1")
	writeAt(t, main, "stele.md", "Rule.\n")
	writeAt(t, wt, "internal/x.go", "package x\n")

	got, err := findRoot(filepath.Join(wt, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	if got != wt {
		t.Errorf("findRoot in a worktree without stele.md = %s, want the worktree %s", got, wt)
	}
}
