package toolpath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lictor-ai/stele/internal/safefile"
)

const sep = string(os.PathListSeparator)

// pathOf returns the value of PATH in env, and whether it is there.
func pathOf(env []string) (string, bool) {
	for _, kv := range env {
		if name, value, _ := strings.Cut(kv, "="); strings.EqualFold(name, "PATH") {
			return value, true
		}
	}
	return "", false
}

func TestEnvFiltersPath(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, "svc", "api")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	out1, out2 := t.TempDir(), t.TempDir()

	tests := []struct {
		name  string
		entry string // dropped
	}{
		{"inside the root", filepath.Join(root, "bin")},
		{"the root itself", root},
		{"inside the enclosing repository", filepath.Join(top, "bin")},
		{"a missing directory inside", filepath.Join(root, "no", "such")},
		{"current directory", "."},
		{"relative", "bin"},
		{"above and back in", filepath.Join(root, "..", "api", "bin")},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := []string{"A=1", "PATH=" + strings.Join([]string{out1, tt.entry, out2}, sep), "B=2"}
			got := Env(env, root)
			want := []string{"A=1", "PATH=" + out1 + sep + out2, "B=2"}
			if !slices.Equal(got, want) {
				t.Errorf("Env = %q, want %q", got, want)
			}
		})
	}
}

func TestEnvKeepsWhatIsOutside(t *testing.T) {
	root, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	env := []string{"PATH=" + b + sep + a}
	if got := Env(env, root); !slices.Equal(got, env) {
		t.Errorf("Env = %q, want %q", got, env)
	}
}

func TestEnvNothingLeft(t *testing.T) {
	root := t.TempDir()
	env := []string{"A=1", "PATH=" + filepath.Join(root, "bin")}
	if got := Env(env, root); !slices.Equal(got, []string{"A=1"}) {
		t.Errorf("Env = %q, want no PATH at all", got)
	}
}

func TestEnvNoRoot(t *testing.T) {
	env := []string{"PATH=." + sep + "bin"}
	if got := Env(env, ""); !slices.Equal(got, env) {
		t.Errorf("Env = %q, want %q", got, env)
	}
}

func TestEnvDoesNotChangeItsInput(t *testing.T) {
	root := t.TempDir()
	env := []string{"PATH=" + filepath.Join(root, "bin"), "A=1"}
	want := slices.Clone(env)
	Env(env, root)
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
}

func TestEnvLeavesOtherVariables(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "bin")
	env := []string{"MYPATH=" + inside, "PATHEXT=.EXE", "=C:=" + inside, "NOEQUALS"}
	if got := Env(env, root); !slices.Equal(got, env) {
		t.Errorf("Env = %q, want %q", got, env)
	}
}

func TestEnvPathNameOnWindows(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "bin")
	env := []string{"Path=" + out + sep + inside}
	got := Env(env, root)
	want := env
	if runtime.GOOS == "windows" {
		want = []string{"Path=" + out} // Windows names it in any case; the rest of the systems do not
	}
	if !slices.Equal(got, want) {
		t.Errorf("Env = %q, want %q", got, want)
	}
}

// A Windows PATH quotes an entry that holds the separator, and SplitList reads
// it as one entry. git ignores the quotes and splits on every ;, so each part
// counts on its own.
func TestEnvQuotedEntry(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows quotes PATH entries")
	}
	root, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	bin := filepath.Join(root, "bin")
	tests := []struct {
		name, entry string
		kept        bool
	}{
		{"every part outside", a + ";" + b, true},
		{"a part inside the project", a + ";" + bin + ";" + b, false},
		{"a relative part", a + ";bin", false},
		{"an empty part", a + ";;" + b, false},
		{"a part of a name inside", filepath.Join(root, "b;in"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pathOf(Env([]string{`PATH="` + tt.entry + `";` + a}, root))
			want := a
			if tt.kept {
				want = `"` + tt.entry + `";` + a
			}
			if !ok || got != want {
				t.Errorf("PATH = %q, want %q", got, want)
			}
		})
	}
}

// dirLink makes link a junction to target on Windows, a symlink elsewhere.
func dirLink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Skipf("mklink /J: %v: %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
}

// sameDir reports whether got is want itself, not a link to it: got is not
// followed. Text cannot tell, as a temp directory can be spelled with an 8.3
// short name on one side and the long name on the other.
func sameDir(t *testing.T, got, want string) bool {
	t.Helper()
	a, err := os.Lstat(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(a, b)
}

// safefile.Resolve lives on the hook's path, where nothing starts a process,
// so its tests with a junction are here.
func TestResolveThroughALink(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	link := filepath.Join(dir, "link")
	dirLink(t, out, link)
	if sameDir(t, link, out) {
		t.Fatal("sameDir followed the link: it could not tell Resolve did nothing")
	}
	rest := filepath.Join("new", "c.txt")
	got, ok := safefile.Resolve(filepath.Join(link, rest))
	base, found := strings.CutSuffix(got, string(filepath.Separator)+rest)
	if !ok || !found || !sameDir(t, base, out) {
		t.Errorf("through a link = %q, %v, want %s below %s", got, ok, rest, out)
	}
}

func TestResolveDanglingLink(t *testing.T) {
	parent := t.TempDir()
	gone := filepath.Join(parent, "gone")
	link := filepath.Join(t.TempDir(), "link")
	dirLink(t, gone, link)
	got, ok := safefile.Resolve(link)
	if !ok || filepath.Base(got) != "gone" || !sameDir(t, filepath.Dir(got), parent) {
		t.Errorf("Resolve = %q, %v, want %q", got, ok, gone)
	}
}

func TestResolveLinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	dirLink(t, b, a)
	dirLink(t, a, b)
	if got, ok := safefile.Resolve(a); ok {
		t.Errorf("Resolve = %q, want it to give up", got)
	}
}

// refused says that both Vet and Env treat dir, as spelled, as inside the
// project at root.
func refused(t *testing.T, dir, root string) {
	t.Helper()
	if _, err := Vet("git", filepath.Join(dir, "git"), root); !errors.Is(err, ErrRefused) {
		t.Errorf("Vet in %s = %v, want ErrRefused", dir, err)
	}
	out := t.TempDir()
	if got, ok := pathOf(Env([]string{"PATH=" + out + sep + dir}, root)); !ok || got != out {
		t.Errorf("PATH with %s = %q, want %q", dir, got, out)
	}
}

// binDir makes a project with a bin directory, and returns both.
func binDir(t *testing.T) (root, bin string) {
	t.Helper()
	root = t.TempDir()
	bin = filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, bin
}

func TestLinkIntoTheProject(t *testing.T) {
	root, bin := binDir(t)
	link := filepath.Join(t.TempDir(), "link")
	dirLink(t, bin, link)
	refused(t, link, root)
	refused(t, filepath.Join(link, "deeper", "still"), root)
}

func TestProjectOpenedThroughALink(t *testing.T) {
	root, bin := binDir(t)
	link := filepath.Join(t.TempDir(), "link")
	dirLink(t, root, link)
	refused(t, bin, link)
	refused(t, filepath.Join(link, "bin"), root)
}

func TestLinkOutOfTheProjectStaysInside(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "result")
	dirLink(t, t.TempDir(), link)
	refused(t, link, root)
}

func TestLinkElsewhereIsOutside(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	dirLink(t, t.TempDir(), link)
	if _, err := Vet("git", filepath.Join(link, "git"), root); err != nil {
		t.Errorf("Vet = %v", err)
	}
	if got, ok := pathOf(Env([]string{"PATH=" + link}, root)); !ok || got != link {
		t.Errorf("PATH = %q, want %q", got, link)
	}
}

// Windows has more spellings of a path than text can compare.
func TestOtherSpellingsOfAProject(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path spellings")
	}
	root, bin := binDir(t)
	refused(t, `\\?\`+bin, root)
	refused(t, `\\.\`+bin, root)
	refused(t, bin, `\\?\`+root)

	vol := filepath.VolumeName(bin)
	share := `\\localhost\` + vol[:1] + `$`
	if _, err := os.Stat(share + `\`); err != nil {
		t.Skipf("no admin share: %v", err)
	}
	refused(t, share+bin[len(vol):], root)
	refused(t, bin, share+root[len(vol):])
}

func TestEnclosingRepositoryThroughALink(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := filepath.Join(top, "svc")
	if err := os.Mkdir(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	dirLink(t, svc, link)
	refused(t, filepath.Join(top, "bin"), link)
}

// The walk up from one entry must not leave a wrong answer for the next: bin
// is inside only through its parent, and the entry below it is met second.
// Only a spelling text cannot compare gets as far as that walk.
func TestEnvEntryBelowOneAlreadyWalked(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path spellings")
	}
	root, bin := binDir(t)
	spelled := `\\?\` + bin
	out := t.TempDir()
	env := []string{"PATH=" + spelled + sep + filepath.Join(spelled, "sub") + sep + out}
	if got, ok := pathOf(Env(env, root)); !ok || got != out {
		t.Errorf("PATH = %q, want %q", got, out)
	}
}

// kept says that Vet and Env treat dir as outside the project at root.
func kept(t *testing.T, dir, root string) {
	t.Helper()
	if _, err := Vet("git", filepath.Join(dir, "git"), root); err != nil {
		t.Errorf("Vet in %s = %v", dir, err)
	}
	if got, ok := pathOf(Env([]string{"PATH=" + dir}, root)); !ok || got != dir {
		t.Errorf("PATH = %q, want %q", got, dir)
	}
}

// A repository at the home directory is not one a project was cloned into:
// ~/.git holds dotfiles, and ~/bin is the user's.
func TestHomeRepositoryDoesNotCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, "notes")
	for _, dir := range []string{filepath.Join(home, ".git"), root} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kept(t, filepath.Join(home, "bin"), root)
	refused(t, filepath.Join(root, "bin"), root)
}

// The home directory is found by identity: HOME can name it through a link.
func TestHomeRepositoryThroughALink(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	dirLink(t, home, link)
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link)
	root := filepath.Join(home, "notes")
	for _, dir := range []string{filepath.Join(home, ".git"), root} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kept(t, filepath.Join(home, "bin"), root)
}

// A repository below the home directory still counts.
func TestRepositoryBelowHomeCounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	top := filepath.Join(home, "work")
	root := filepath.Join(top, "svc")
	for _, dir := range []string{filepath.Join(top, ".git"), root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	refused(t, filepath.Join(top, "bin"), root)
}

// /.git in a container must not take /usr/bin into every project.
func TestFilesystemRootIsNoRepository(t *testing.T) {
	root := t.TempDir()
	drive := filepath.VolumeName(root) + string(filepath.Separator)
	if !homeOrFSRoot(drive) {
		t.Errorf("homeOrFSRoot(%s) = false", drive)
	}
	if homeOrFSRoot(root) {
		t.Errorf("homeOrFSRoot(%s) = true", root)
	}
}

// A link to a project in another repository counts that repository, not the
// one the link happens to sit in.
func TestLinkedRootCountsOnlyItsRealRepository(t *testing.T) {
	lexical, real := t.TempDir(), t.TempDir()
	svc := filepath.Join(real, "svc")
	for _, dir := range []string{filepath.Join(lexical, ".git"), filepath.Join(real, ".git"), svc} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(lexical, "svc")
	dirLink(t, svc, link)
	kept(t, filepath.Join(lexical, "bin"), link)
	refused(t, filepath.Join(real, "bin"), link)
}

// A root whose links cannot be followed is judged as it is spelled.
func TestRootThatCannotBeResolved(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(top, "a"), filepath.Join(top, "b")
	dirLink(t, b, a)
	dirLink(t, a, b)
	if _, err := Vet("git", filepath.Join(top, "bin", "git"), a); !errors.Is(err, ErrRefused) {
		t.Errorf("Vet = %v, want ErrRefused", err)
	}
}

// A dotfiles repository in the home directory must not take ~/bin for part
// of a project below it: only the nearest repository counts.
func TestOnlyTheNearestRepositoryCounts(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "work", "proj")
	for _, dir := range []string{filepath.Join(outer, ".git"), filepath.Join(root, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(outer, "bin")
	if _, err := Vet("git", filepath.Join(bin, "git"), root); err != nil {
		t.Errorf("Vet = %v", err)
	}
	if got, ok := pathOf(Env([]string{"PATH=" + bin}, root)); !ok || got != bin {
		t.Errorf("PATH = %q, want %q", got, bin)
	}
}

func TestVet(t *testing.T) {
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, "svc")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, found := range []string{filepath.Join(root, "bin", "git"), filepath.Join(top, "bin", "git")} {
		if _, err := Vet("git", found, root); !errors.Is(err, ErrRefused) {
			t.Errorf("Vet(%s) = %v, want ErrRefused", found, err)
		} else if !strings.Contains(filepath.ToSlash(err.Error()), filepath.ToSlash(found)) {
			t.Errorf("err = %q, want it to name %s", err, found)
		}
	}
	found := filepath.Join(t.TempDir(), "git")
	got, err := Vet("git", found, root)
	if err != nil || got != found {
		t.Errorf("Vet = %q, %v, want %q", got, err, found)
	}
}
