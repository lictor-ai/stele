package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// cwd is where the table's commands run: a directory that need not exist,
// since Targets reads no file but for globs.
var cwd = func() string {
	if runtime.GOOS == "windows" {
		return `C:\p`
	}
	return "/p"
}()

// at is a target in cwd.
func at(rel string, k Kind) Target {
	return Target{Path: filepath.Join(cwd, filepath.FromSlash(rel)), Kind: k}
}

// env replaces the environment the lexer reads for the test.
func env(t *testing.T, vars map[string]string) {
	t.Helper()
	old := getenv
	getenv = func(k string) string { return vars[k] }
	t.Cleanup(func() { getenv = old })
}

func TestTargets(t *testing.T) {
	home := filepath.Join(cwd, "home")
	env(t, map[string]string{"HOME": home})
	R, T, S, P := Redirect, Tee, SedInPlace, PerlInPlace
	tests := []struct {
		os      string // "" for every system, else "windows" or "unix"
		command string
		want    []Target
		unknown int
	}{
		// Redirections.
		{command: "echo hi > a.go", want: []Target{at("a.go", R)}},
		{command: "echo hi >a.go", want: []Target{at("a.go", R)}},
		{command: "echo hi>a.go", want: []Target{at("a.go", R)}},
		{command: "echo hi >> a.go", want: []Target{at("a.go", R)}},
		{command: "echo hi >| a.go", want: []Target{at("a.go", R)}},
		{command: "echo > f1 > f2", want: []Target{at("f1", R), at("f2", R)}},
		{command: "cmd &> out.log", want: []Target{at("out.log", R)}},
		{command: "cmd >& out.log", want: []Target{at("out.log", R)}},
		{command: "cmd &>> out.log", want: []Target{at("out.log", R)}},
		{command: "cmd 2> err.txt", want: []Target{at("err.txt", R)}},
		{command: "cmd <> rw.txt", want: []Target{at("rw.txt", R)}},
		{command: "cmd > a.go &", want: []Target{at("a.go", R)}},
		{command: "exec > log", want: []Target{at("log", R)}},
		{command: "a2>f", want: []Target{at("f", R)}},

		// Heredocs: the body is never read as commands.
		{command: "cat > a.go <<'EOF'\nx > y.go\nEOF", want: []Target{at("a.go", R)}},
		{command: "cat <<EOF > a.go\nfoo > bar\nEOF\necho > b.go", want: []Target{at("a.go", R), at("b.go", R)}},
		{command: "cat <<-EOF\n\tx > c\n\tEOF", want: nil},
		{command: "cat <<A <<B\n>a\nA\n>b\nB", want: nil},
		{command: "cat <<EOF\n> x", want: nil},
		{command: "cat <<\"EOF\" | tee out.go\n> x\nEOF", want: []Target{at("out.go", T)}},

		// Not a write.
		{command: `echo "a > b"`, want: nil},
		{command: `echo 'a > b'`, want: nil},
		{command: `echo a \> b`, want: nil},
		{command: `git commit -m "a > b"`, want: nil},
		{command: "[[ a > b ]]", want: nil},
		{command: "(( a > b ))", want: nil},
		{command: "echo $(( 1 > 2 ))", want: nil},
		{command: "[[ $x > b ]] && echo > c.go", want: []Target{at("c.go", R)}},
		{command: "[ a > b ]", want: []Target{at("b", R)}},
		{command: "go test ./... 2>&1", want: nil},
		{command: "cmd >&2", want: nil},
		{command: "cmd 2>&-", want: nil},
		{command: "make > /dev/null 2>&1", want: nil},
		{command: "cmd < in.txt", want: nil},
		{command: "cat <<< 'x > y'", want: nil},
		{os: "windows", command: "x > NUL", want: nil},
		{os: "windows", command: "x > nul", want: nil},

		// Variables, substitutions, globs, home.
		{command: "echo x > $F", unknown: 1},
		{command: "F=a.go; echo > $F", want: []Target{at("a.go", R)}},
		{command: "export F=b.go && echo > ${F}", want: []Target{at("b.go", R)}},
		{command: `echo > "$HOME/x"`, want: []Target{{Path: filepath.Join(home, "x"), Kind: R}}},
		{command: "echo > ~/a", want: []Target{{Path: filepath.Join(home, "a"), Kind: R}}},
		{command: "echo > ~bob/a", unknown: 1},
		{command: "echo > $(mktemp)", unknown: 1},
		{command: "echo > ${x:-y}", unknown: 1},
		{command: "echo > $1", unknown: 1},
		{command: "echo $(echo y > in.go)", want: []Target{at("in.go", R)}},
		{command: "echo `echo y > t.go`", want: []Target{at("t.go", R)}},
		{command: `echo "$(echo y > q.go)"`, want: []Target{at("q.go", R)}},
		{command: "echo x | tee >(cat > p.go) > /dev/null", want: []Target{at("p.go", R)}},

		// tee.
		{command: "tee a.go b.go < in", want: []Target{at("a.go", T), at("b.go", T)}},
		{command: "tee -a log", want: []Target{at("log", T)}},
		{command: "cmd | tee", want: nil},
		{command: "tee -- -w", want: []Target{at("-w", T)}},
		{command: "tee --output-error=warn f", want: []Target{at("f", T)}},
		{os: "unix", command: "sudo tee /etc/x", want: []Target{{Path: "/etc/x", Kind: T}}},
		{command: "sudo -u bob tee f", want: []Target{at("f", T)}},
		{command: "env A=1 tee f", want: []Target{at("f", T)}},
		{command: "FOO=bar tee f", want: []Target{at("f", T)}},
		{command: "nohup nice -n 5 tee f", want: []Target{at("f", T)}},
		{command: "/usr/bin/tee f", want: []Target{at("f", T)}},

		// sed -i and perl -i.
		{command: "sed -i 's/a/b/' a.go b.go", want: []Target{at("a.go", S), at("b.go", S)}},
		{command: "sed -i.bak -e s/a/b/ a.go", want: []Target{at("a.go", S)}},
		{command: "sed -Ei s/x/y/ f", want: []Target{at("f", S)}},
		{command: "sed --in-place=.b s/x/y/ f", want: []Target{at("f", S)}},
		{command: "sed --in-place --expression=s/x/y/ f", want: []Target{at("f", S)}},
		{command: "sed -i '' s/x/y/ f", want: []Target{at("f", S)}},
		{command: "sed -ie s/a/b/ x.go", want: []Target{at("x.go", S)}},
		{command: "sed -n 's/a/b/p' a.go", want: nil},
		{command: "sed s/x/y/ f > f2", want: []Target{at("f2", R)}},
		{command: "perl -pi -e 's/a/b/' a.go", want: []Target{at("a.go", P)}},
		{command: "perl -i.bak -pe 's/a/b/' a.go", want: []Target{at("a.go", P)}},
		{command: "perl -0777 -pi -e 's/a/b/' a.go", want: []Target{at("a.go", P)}},
		{command: "perl -i script.pl a.go", want: []Target{at("a.go", P)}},
		{command: "perl -ne print a.go", want: nil},
		{command: "perl s.pl a.go", want: nil},

		// The directory moves with cd.
		{command: "cd sub && echo > a.go", want: []Target{at("sub/a.go", R)}},
		{command: "cd sub; cd ..; echo > a.go", want: []Target{at("a.go", R)}},
		{command: "(cd sub && echo > a.go); echo > b.go", want: []Target{at("sub/a.go", R), at("b.go", R)}},
		{command: `cd "$X" && echo > a.go`, unknown: 1},
		{command: "cd - && echo > a.go", unknown: 1},
		{command: "cd sub | true; echo > a.go", want: []Target{at("a.go", R)}},
		{command: "{ cd sub; echo > a.go; }", want: []Target{at("sub/a.go", R)}},

		// Comments, quoting, keywords.
		{command: "# echo > a.go", want: nil},
		{command: "echo a#b > c", want: []Target{at("c", R)}},
		{command: `echo > "a b.go"`, want: []Target{at("a b.go", R)}},
		{command: `echo >out\ file`, want: []Target{at("out file", R)}},
		{command: `$'x' > f`, want: []Target{at("f", R)}},
		{command: "if true; then echo > a.go; fi", want: []Target{at("a.go", R)}},
		{command: "for f in x; do echo > $f; done", unknown: 1},
		{command: "case x in a) echo > a.go;; esac", want: []Target{at("a.go", R)}},
		{command: "echo one \\\n  > a.go", want: []Target{at("a.go", R)}},
		{command: "echo x > a.go\r\necho y > a.go", want: []Target{at("a.go", R)}},

		// PWD is the directory the command has moved to, not stele's.
		{command: "echo > $PWD/a.go", want: []Target{at("a.go", R)}},
		{command: "cd sub && echo > ${PWD}/a.go", want: []Target{at("sub/a.go", R)}},
		{command: "cd $X; echo > $PWD/a.go", unknown: 1},
		{command: "echo > $OLDPWD/a.go", unknown: 1},

		// An unquoted heredoc's body runs its substitutions; a quoted one's does not.
		{command: "cat <<EOF\n$(echo x > in.go)\n`echo y > bt.go`\nEOF\necho > after.go", want: []Target{at("in.go", R), at("bt.go", R), at("after.go", R)}},
		{command: "cat <<'EOF'\n$(echo x > in.go)\nEOF", want: nil},
		{command: "cat <<EOF\n\\$(echo x > no.go)\nEOF", want: nil},

		// A function body's cd stays in the function; its writes still count.
		{command: "f() { cd sub; echo > in.go; }; echo > out.go", want: []Target{at("sub/in.go", R), at("out.go", R)}},
		{command: "function f { cd sub; }; echo > out.go", want: []Target{at("out.go", R)}},
		{command: "function f() { cd sub; }\necho > out.go", want: []Target{at("out.go", R)}},

		// A case pattern's ) closes nothing.
		{command: "(case x in a) cd sub;; (b) true;; esac; echo > in.go); echo > out.go", want: []Target{at("sub/in.go", R), at("out.go", R)}},
		{command: "case $x in *.go) echo > g;; *) echo > h;; esac", want: []Target{at("g", R), at("h", R)}},

		// More wrappers.
		{command: "timeout 10 tee f", want: []Target{at("f", T)}},
		{command: "timeout -s KILL --kill-after=5 1m sed -i s/a/b/ f", want: []Target{at("f", S)}},
		{command: "stdbuf -oL tee f", want: []Target{at("f", T)}},
		{command: "stdbuf -o L tee f", want: []Target{at("f", T)}},
		{command: "ionice -c 3 tee f", want: []Target{at("f", T)}},

		// $[ … ] is arithmetic.
		{command: "echo $[ 1 > 2 ]", want: nil},
		{command: "echo $[1>2] > f", want: []Target{at("f", R)}},

		// Removals and copies, which only matter for stele's own files.
		{command: "rm -f stele.json", want: []Target{at("stele.json", Remove)}},
		{command: "rm -rf -- .stele", want: []Target{at(".stele", Remove)}},
		{command: "mv a stele.md", want: []Target{at("a", Remove), at("stele.md", Copy)}},
		{command: "cp x stele.lock", want: []Target{at("stele.lock", Copy)}},
		{command: "cp -t sub x y", want: []Target{at("sub/x", Copy), at("sub/y", Copy)}},
		{command: "ln -s x stele.md", want: []Target{at("stele.md", Copy)}},
		{command: "truncate -s0 f", want: []Target{at("f", Copy)}},
		{command: "truncate -s 0 f", want: []Target{at("f", Copy)}},

		// What the lexer does not see.
		{command: "xargs sed -i s/a/b/", want: nil},
		{command: `python -c "open('x','w')"`, want: nil},
		{command: `echo "abc > f`, want: nil},
		{command: "echo `unterminated > f", want: nil},

		// Paths.
		{os: "unix", command: "echo > /tmp/x", want: []Target{{Path: "/tmp/x", Kind: R}}},
		{os: "windows", command: "echo > /c/U/a.go", want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: `echo > 'C:\U\a.go'`, want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: "echo > C:/U/a.go", want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: "cd /c/o && echo > a", want: []Target{{Path: `C:\o\a`, Kind: R}}},
		{os: "windows", command: "echo > /tmp/x", unknown: 1},
		{os: "windows", command: `echo > C:\Users\x`, unknown: 1},
	}
	for _, tt := range tests {
		if tt.os == "windows" && runtime.GOOS != "windows" || tt.os == "unix" && runtime.GOOS == "windows" {
			continue
		}
		got, unknown := Targets(tt.command, cwd)
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", tt.command, diff)
		}
		if unknown != tt.unknown {
			t.Errorf("Targets(%q): unknown = %d, want %d", tt.command, unknown, tt.unknown)
		}
	}
}

// With no directory to resolve against, a relative target is unknown.
func TestTargetsNoCwd(t *testing.T) {
	got, unknown := Targets("echo > a", "")
	if len(got) != 0 || unknown != 1 {
		t.Errorf("Targets = %v, unknown %d; want none, 1 unknown", got, unknown)
	}
}

// Targets reads no file but for globs: a directory that does not exist is
// as good as any.
func TestTargetsReadsNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no", "such", "dir")
	got, _ := Targets("cd sub && cat > a.go <<EOF\nx\nEOF", dir)
	want := []Target{{Path: filepath.Join(dir, "sub", "a.go"), Kind: Redirect}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestTargetsGlob(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Targets("sed -i s/a/b/ src/*.go", dir)
	want := []Target{
		{Path: filepath.Join(dir, "src", "a.go"), Kind: SedInPlace},
		{Path: filepath.Join(dir, "src", "b.go"), Kind: SedInPlace},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("with matches (-want +got):\n%s", diff)
	}
	// No match: the word stays as written, as in bash.
	got, _ = Targets("sed -i s/a/b/ src/*.ts", dir)
	want = []Target{{Path: filepath.Join(dir, "src", "*.ts"), Kind: SedInPlace}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("no match (-want +got):\n%s", diff)
	}
	// A quoted star is a character.
	got, _ = Targets(`echo > "src/*.go"`, dir)
	want = []Target{{Path: filepath.Join(dir, "src", "*.go"), Kind: Redirect}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("quoted (-want +got):\n%s", diff)
	}
}

// A glob reads at most a budget of directory entries: past it, its target is
// unknown, so a glob over a whole drive costs no more than a small one.
func TestTargetsGlobBudget(t *testing.T) {
	old := maxGlobEntries
	maxGlobEntries = 100
	t.Cleanup(func() { maxGlobEntries = old })
	dir := t.TempDir()
	for i := range 15 {
		for j := range 15 {
			if err := os.MkdirAll(filepath.Join(dir, fmt.Sprint("d", i), fmt.Sprint("e", j)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := time.Now()
	got, unknown := Targets("rm -f */*/*.xyz; echo > */*/*/*.q", dir)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("globs over the budget took %v", d)
	}
	if len(got) != 0 || unknown != 2 {
		t.Errorf("over the budget: %v, unknown %d; want none, 2 unknown", got, unknown)
	}
	// Within it, a glob still matches.
	if got, _ := Targets("echo > d1/*", dir); len(got) != 15 {
		t.Errorf("within the budget: %d matches, want 15", len(got))
	}
}

// The caps bound what one command costs.
func TestTargetsCaps(t *testing.T) {
	if got, unknown := Targets(strings.Repeat("x", maxCommand+1), cwd); got != nil || unknown != 1 {
		t.Errorf("a command over the cap: %v, unknown %d", got, unknown)
	}
	var b strings.Builder
	for i := range maxTargets + 10 {
		b.WriteString("echo > f")
		b.WriteString(strings.Repeat("x", i))
		b.WriteString("; ")
	}
	if got, _ := Targets(b.String(), cwd); len(got) != maxTargets {
		t.Errorf("%d targets, want the cap %d", len(got), maxTargets)
	}
	deep := strings.Repeat("$(", 100) + "echo > f" + strings.Repeat(")", 100)
	if got, _ := Targets(deep, cwd); len(got) != 0 {
		t.Errorf("past the nesting cap: %v", got)
	}
	// A repeated target counts once.
	if got, _ := Targets("echo > a; echo >> a; tee a", cwd); len(got) != 1 {
		t.Errorf("duplicates: %v", got)
	}
}

func TestAbs(t *testing.T) {
	if runtime.GOOS == "windows" {
		for word, want := range map[string]string{
			"/c/U/a": `C:\U\a`, "/C": `C:\`, `C:\x\..\y`: `C:\y`, "C:/a": `C:\a`, "a": `C:\p\a`,
		} {
			if got, ok := Abs(`C:\p`, word); !ok || got != want {
				t.Errorf("Abs(%q) = %q, %v; want %q", word, got, ok, want)
			}
		}
		for _, word := range []string{"/tmp/x", "//host/x", `\x`, "C:foo", ""} {
			if got, ok := Abs(`C:\p`, word); ok {
				t.Errorf("Abs(%q) = %q, want unknown", word, got)
			}
		}
		return
	}
	for word, want := range map[string]string{"/a/../b": "/b", "a": "/p/a", "./a/": "/p/a"} {
		if got, ok := Abs("/p", word); !ok || got != want {
			t.Errorf("Abs(%q) = %q, %v; want %q", word, got, ok, want)
		}
	}
	if _, ok := Abs("", "a"); ok {
		t.Error("a relative path with no directory resolved")
	}
}

// Targets never panics, and its time grows with the command, not faster.
func FuzzTargets(f *testing.F) {
	for _, s := range []string{
		"cat > a.go <<'EOF'\nx > y\nEOF", "echo $(echo `a > b` > c) > d", "[[ a > b ]] && (( 1 > 2 ))",
		"sed -i -e s/a/b/ f", "perl -0777 -pi -e x f", "cd a; (cd b && tee c) | tee d", "${", "$((", "<(", "'",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		start := time.Now()
		Targets(s, cwd)
		if d := time.Since(start); d > time.Second {
			t.Errorf("Targets took %v on %d bytes", d, len(s))
		}
	})
}

func BenchmarkTargets(b *testing.B) {
	var sb strings.Builder
	for sb.Len() < 10<<10 {
		sb.WriteString("cat > internal/x.go <<'EOF'\npackage x\n\nfunc f() { if a > b { return } }\nEOF\n")
		sb.WriteString(`go test ./... 2>&1 | tee out.log && sed -i "s/a/b/" a.go; echo "x > y"` + "\n")
	}
	cmd := sb.String()
	b.ResetTimer()
	for range b.N {
		Targets(cmd, cwd)
	}
}
