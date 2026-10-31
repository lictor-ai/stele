package toolpath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
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

// A Windows PATH quotes an entry that holds the separator; the entry must
// stay one entry.
func TestEnvKeepsAQuotedEntry(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows quotes PATH entries")
	}
	root, out := t.TempDir(), t.TempDir()
	semi := filepath.Join(out, "a;b")
	inside := filepath.Join(root, "b;in")
	got, ok := pathOf(Env([]string{`PATH="` + semi + `";"` + inside + `";` + out}, root))
	if want := `"` + semi + `";` + out; !ok || got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

func TestEnvDropsALinkIntoTheProject(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(out, "link")
	if err := os.Symlink(bin, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	got, ok := pathOf(Env([]string{"PATH=" + out + sep + link}, root))
	if !ok || got != out {
		t.Errorf("PATH = %q, want %q", got, out)
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
