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

		// A function body may be a subshell.
		{command: "f() ( echo x > d.go ); echo > e.go", want: []Target{at("d.go", R), at("e.go", R)}},
		{command: "f() ( cd sub; echo > in.go ); echo > out.go", want: []Target{at("sub/in.go", R), at("out.go", R)}},
		{command: "f ( ) ( echo > d.go )", want: []Target{at("d.go", R)}},
		{command: "function f ( echo > d.go )", want: []Target{at("d.go", R)}},
		{command: "function f() ( echo > d.go )", want: []Target{at("d.go", R)}},
		{command: "function f ( ) ( echo > d.go )", want: []Target{at("d.go", R)}},
		{command: "f()\n( echo > d.go )", want: []Target{at("d.go", R)}},
		{command: "f() ((1)); { cd sub; }; echo > a", want: []Target{at("sub/a", R)}},
		{command: "f() ( cd a ); { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() ( > x ); { cd sub; }; echo > c.go", want: []Target{at("x", R), at("sub/c.go", R)}},

		// Any other compound body: the braces after it are not a function's.
		{command: "f() [[ x ]]; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() case x in esac; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() if true; then :; fi; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() while false; do :; done; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() until true; do :; done; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() for x in y; do :; done; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() select x in y; do :; done; { cd sub; }; echo > c.go", want: []Target{at("sub/c.go", R)}},
		{command: "f() if { cd a; }; then :; fi; echo > c.go", want: []Target{at("c.go", R)}},

		// An array assignment: what follows it is read, and its value is not
		// known.
		{command: "arr=(1 2); echo x > b.go", want: []Target{at("b.go", R)}},
		{command: "declare -a v=(1); rm stele.md", want: []Target{at("stele.md", Remove)}},
		{command: "x+=(1); echo > b.go", want: []Target{at("b.go", R)}},
		// bash stops these two when it runs them, "cannot assign list to array
		// member", so echo never runs: seeing b.go only denies once too often.
		{command: "a[0]=(1); echo > b.go", want: []Target{at("b.go", R)}},
		{command: "a[b[1]]+=(1); echo > b.go", want: []Target{at("b.go", R)}},
		{command: "a=(1 2) tee b.go", want: []Target{at("b.go", T)}},
		{command: "a[0]=(1) tee b.go", want: []Target{at("b.go", T)}},
		{command: "a=1 b=(2) tee f", want: []Target{at("f", T)}},
		{command: "a=() > f", want: []Target{at("f", R)}},
		{command: "FOO=1 declare v=(1); echo > b.go", want: []Target{at("b.go", R)}},
		{command: "export -n v=(1) w=(2); echo > b.go", want: []Target{at("b.go", R)}},
		{command: "a=($(echo y > in.go) `echo z > t.go`); echo > b.go", want: []Target{at("in.go", R), at("t.go", R), at("b.go", R)}},
		{command: "a=(<(echo y > p.go))", want: []Target{at("p.go", R)}},
		{command: "a=(\n  1 # c > x\n  2\n)\necho > b.go", want: []Target{at("b.go", R)}},
		{command: "a=([k]=v)", want: nil},
		{command: "a=(1); echo > $a", unknown: 1},
		{command: `a=(1); echo > "c$a"`, unknown: 1},
		{command: "declare a=(1); echo > c${a}", unknown: 1},
		{command: "a=1; a+=2; echo > $a", unknown: 1},
		{command: "a=1; a+=2; echo > c$a", unknown: 1},
		{command: "a=b; a[0]=c; echo > c$a", unknown: 1},
		{command: "x+=1 tee f", want: []Target{at("f", T)}},
		{command: "a[1]=x tee f", want: []Target{at("f", T)}},
		{command: "tee a+=b", want: []Target{at("a+=b", T)}},
		{command: "[[ $x =~ a=(b|c) ]] && echo > c.go", want: []Target{at("c.go", R)}},

		// Syntax errors in bash too: nothing after one runs.
		{command: "echo a ( b > c", want: nil},
		{command: "arr= (1); echo > b.go", want: nil},
		{command: "a=b=(1); echo > b.go", want: nil},
		{command: "echo a=(1); echo > b.go", want: nil},
		{command: "a=1; echo c=(1); echo > b.go", want: nil},
		{command: "sudo a=(1); echo > b.go", want: nil},
		{command: "command declare v=(1); echo > b.go", want: nil},
		{command: `"declare" v=(1); echo > b.go`, want: nil},
		{command: `"a"=(1); echo > b.go`, want: nil},
		{command: "a=(x > y); echo > b.go", want: nil},
		{command: "a=(1; echo > b.go)", want: nil},
		{command: "a=( (1) ); echo > b.go", want: nil},
		{command: "a=(1", want: nil},

		// The time keyword takes one -p, then one --; what follows it is the
		// command.
		{command: "time tee f", want: []Target{at("f", T)}},
		{command: "time -p tee a.go", want: []Target{at("a.go", T)}},
		{command: "time -- tee f", want: []Target{at("f", T)}},
		{command: "time -p -- tee f", want: []Target{at("f", T)}},
		{command: "time -p { tee f; }", want: []Target{at("f", T)}},
		{command: "time (tee f)", want: []Target{at("f", T)}},
		{command: "time -p (tee f)", want: []Target{at("f", T)}},
		{command: "! time -p tee f", want: []Target{at("f", T)}},
		{command: "time -p; tee f", want: []Target{at("f", T)}},
		{command: "time -p -p tee f", want: nil},
		{command: "time -- -p tee f", want: nil},
		{command: "time -pp tee f", want: nil},
		{command: "time -ptee f", want: nil},
		{command: "time --tee f", want: nil},
		{command: "time -v tee f", want: nil},
		{command: "/usr/bin/time -o log -f %e tee f", want: []Target{at("f", T)}},

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
		// Valid bash that stele cannot parse: it reads up to there.
		{command: "a=\\\n(1); echo > b.go", want: nil},
		{command: "a[ 1 ]=(1); echo > b.go", want: nil},
		{command: "a[(1)]=x; echo > b.go", want: nil},
		// (( is read as arithmetic, though bash falls back to nested subshells.
		{command: "((echo > d.go) && (echo > e.go))", want: nil},
		{command: "((echo > d.go) && (echo > e.go)); echo > f.go", want: []Target{at("f.go", R)}},
		// With no )) it reads to the end, so what follows it is lost too.
		{command: "((echo x > a.go) || true) && echo z > c.go", want: nil},
		// A cd in a body that is not in braces or a subshell moves the directory,
		// though bash only defines the function: it writes c.go here.
		{command: "f() if true; then cd a; fi; echo > c.go", want: []Target{at("a/c.go", R)}},
		{command: "f() case x in x) cd a;; esac; echo > c.go", want: []Target{at("a/c.go", R)}},
		{command: "f() for x in y; do cd a; done; echo > c.go", want: []Target{at("a/c.go", R)}},
		{command: "f() while cd a; do break; done; echo > c.go", want: []Target{at("a/c.go", R)}},

		// Paths.
		{os: "unix", command: "echo > /tmp/x", want: []Target{{Path: "/tmp/x", Kind: R}}},
		{os: "windows", command: "echo > /c/U/a.go", want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: `echo > 'C:\U\a.go'`, want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: "echo > C:/U/a.go", want: []Target{{Path: `C:\U\a.go`, Kind: R}}},
		{os: "windows", command: "cd /c/o && echo > a", want: []Target{{Path: `C:\o\a`, Kind: R}}},
		{os: "windows", command: "echo > /tmp/x", unknown: 1},
		{os: "windows", command: `echo > C:\Users\x`, unknown: 1},

		// A name joined to a directory given with -t.
		{command: "cp -t sub/ x", want: []Target{at("sub/x", Copy)}},
		{command: "cp -t '' x", want: []Target{at("x", Copy)}},
		{os: "unix", command: "cp -t / x", want: []Target{{Path: "/x", Kind: Copy}}},
		{os: "unix", command: `cp -t 'a\' x`, want: []Target{at(`a\/x`, Copy)}},
		{os: "unix", command: "cp -t a: x", want: []Target{at("a:/x", Copy)}},
		{os: "windows", command: `cp -t 'a\' x`, want: []Target{at("a/x", Copy)}},
		{os: "windows", command: `cp -t 'C:\' x`, want: []Target{{Path: `C:\x`, Kind: Copy}}},
		{os: "windows", command: "cp -t C:/ x", want: []Target{{Path: `C:\x`, Kind: Copy}}},
		{os: "windows", command: "cp -t C: x", unknown: 1},
		{os: "windows", command: "cp -t /c x", want: []Target{{Path: `C:\x`, Kind: Copy}}},
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

// A backslash in a directory separates on Windows only, so a .. after it goes
// up one level there: joining a name must leave that to Abs.
func TestTargetsCopyBackslashDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		command       string
		windows, unix string
	}{
		{`cp x 'a\b/../c'`, "a/c/x", "c/x"},
		{`cp -t 'a\b/../c' x`, "a/c/x", "c/x"},
		{`cp x 'a\b/..'`, "a/x", "x"},
	}
	for _, tt := range tests {
		rel := tt.unix
		if runtime.GOOS == "windows" {
			rel = tt.windows
		}
		want := []Target{{Path: filepath.Join(dir, filepath.FromSlash(rel)), Kind: Copy}}
		got, _ := Targets(tt.command, dir)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", tt.command, diff)
		}
	}
}

// A glob's * and ? skip a name starting with a dot, unless the pattern starts
// with one, and a pattern ending in / matches directories, as in bash.
func TestTargetsGlobNames(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"dst", "bak/sub"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bak/.hidden", "bak/plain"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(f)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := func(rel string, k Kind) Target {
		return Target{Path: filepath.Join(dir, filepath.FromSlash(rel)), Kind: k}
	}
	tests := []struct {
		command string
		want    []Target
	}{
		{"rm bak/*", []Target{file("bak/plain", Remove), file("bak/sub", Remove)}},
		{"rm bak/.*", []Target{file("bak/.hidden", Remove)}},
		{"rm bak/?hidden", []Target{file("bak/?hidden", Remove)}},
		{"rm bak/[.]hidden", []Target{file("bak/[.]hidden", Remove)}},
		{"cp bak/* dst/", []Target{file("dst/plain", Copy), file("dst/sub", Copy)}},
		{"cp -r bak/*/ dst/", []Target{file("dst/sub", Copy)}},
		{"rm -r bak/*/", []Target{file("bak/sub", Remove)}},
		{"rm -r bak/p*/", []Target{file("bak/p*", Remove)}},
		// A fixed last segment must exist.
		{"rm b*/plain", []Target{file("bak/plain", Remove)}},
		{"rm b*/none", []Target{file("b*/none", Remove)}},
	}
	for _, tt := range tests {
		got, unknown := Targets(tt.command, dir)
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", tt.command, diff)
		}
		if unknown != 0 {
			t.Errorf("Targets(%q): unknown = %d", tt.command, unknown)
		}
	}
}

// A bracket set takes ! as bash does, and a ] first in it is a member.
func TestTargetsGlobSets(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"_x.go", "a.go", ".h.go", "!x", "]x"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		os      string // "" for every system, else "windows" or "unix"
		command string
		want    []string
	}{
		{command: "rm [!_]*.go", want: []string{"a.go"}},
		{command: "rm [^_]*.go", want: []string{"a.go"}},
		{command: "rm [!a-z]*.go", want: []string{"_x.go"}},
		{command: "rm [_a]*.go", want: []string{"_x.go", "a.go"}},
		{command: "rm x[!]", want: []string{"x[!]"}},
		{command: "rm [a!]x", want: []string{"!x"}},
		{command: "rm [!x", want: []string{"[!x"}},
		{os: "unix", command: "rm []]x", want: []string{"]x"}},
		{os: "unix", command: "rm [!]]x", want: []string{"!x"}},
		{os: "unix", command: "rm []!]x", want: []string{"!x", "]x"}},
		// filepath.Match has no escape on Windows, so no ] in a set: such a
		// pattern matches nothing there and stays as written.
		{os: "windows", command: "rm []]x", want: []string{"[]]x"}},
	}
	for _, tt := range tests {
		if tt.os == "windows" && runtime.GOOS != "windows" || tt.os == "unix" && runtime.GOOS == "windows" {
			continue
		}
		var want []Target
		for _, f := range tt.want {
			want = append(want, Target{Path: filepath.Join(dir, f), Kind: Remove})
		}
		got, unknown := Targets(tt.command, dir)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", tt.command, diff)
		}
		if unknown != 0 {
			t.Errorf("Targets(%q): unknown = %d", tt.command, unknown)
		}
	}
	if got, _ := Targets("sed -i s/a/b/ [!_]*.go", dir); len(got) != 1 || got[0].Kind != SedInPlace {
		t.Errorf("sed: %v", got)
	}
}

func TestChild(t *testing.T) {
	sep := string(filepath.Separator)
	for dir, want := range map[string]string{sep: sep + "a", sep + "b": sep + "b" + sep + "a"} {
		if got := child(dir, "a"); got != want {
			t.Errorf("child(%q, a) = %q, want %q", dir, got, want)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	old := windows
	t.Cleanup(func() { windows = old })
	for _, tt := range []struct{ seg, unix, windows string }{
		{"*.go", "*.go", "*.go"},
		{"[!_]*.go", "[^_]*.go", "[^_]*.go"},
		{"[^_]x", "[^_]x", "[^_]x"},
		{"[a!]", "[a!]", "[a!]"},
		{"a[!b]c[!d]", "a[^b]c[^d]", "a[^b]c[^d]"},
		{"[]]x", `[\]]x`, "[]]x"},
		{"[!]]x", `[^\]]x`, "[^]]x"},
		{"[]!]x", `[\]!]x`, "[]!]x"},
		{"[!x", "[!x", "[!x"},
		{"[!]", "[!]", "[!]"},
		{"[]", "[]", "[]"},
		{"[![!x]", "[^[!x]", "[^[!x]"},
		{`\[!x]`, `\[!x]`, `\[^x]`},
		{`[!\]]x`, `[^\]]x`, `[^\]]x`},
		{`[a\]!]`, `[a\]!]`, `[a\]!]`},
		{`[\][!a]`, `[\][!a]`, `[\][^a]`},
		{`a\`, `a\`, `a\`},
	} {
		windows = false
		if got := matchPattern(tt.seg); got != tt.unix {
			t.Errorf("matchPattern(%q) = %q, want %q", tt.seg, got, tt.unix)
		}
		windows = true
		if got := matchPattern(tt.seg); got != tt.windows {
			t.Errorf("on Windows, matchPattern(%q) = %q, want %q", tt.seg, got, tt.windows)
		}
	}
}

// A glob source copied into a directory lands under the names it matches.
func TestTargetsCopyGlob(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", ".claude"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"settings.json", "x.go"} {
		if err := os.WriteFile(filepath.Join(dir, "a", f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := func(d, f string) string { return filepath.Join(dir, d, f) }
	tests := []struct {
		command string
		want    []Target
	}{
		{"cp a/* .claude/", []Target{
			{Path: in(".claude", "settings.json"), Kind: Copy},
			{Path: in(".claude", "x.go"), Kind: Copy},
		}},
		{"mv a/* .claude/", []Target{
			{Path: in("a", "settings.json"), Kind: Remove},
			{Path: in(".claude", "settings.json"), Kind: Copy},
			{Path: in("a", "x.go"), Kind: Remove},
			{Path: in(".claude", "x.go"), Kind: Copy},
		}},
		{"cp -t .claude a/*.json", []Target{{Path: in(".claude", "settings.json"), Kind: Copy}}},
		// Quoted, or matching nothing, the word stays as written, as in bash.
		{"cp 'a/*' .claude/", []Target{{Path: in(".claude", "*"), Kind: Copy}}},
		{"cp a/*.zz .claude/", []Target{{Path: in(".claude", "*.zz"), Kind: Copy}}},
		{"mv a/*.zz .claude/", []Target{
			{Path: in("a", "*.zz"), Kind: Remove},
			{Path: in(".claude", "*.zz"), Kind: Copy},
		}},
	}
	for _, tt := range tests {
		got, unknown := Targets(tt.command, dir)
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", tt.command, diff)
		}
		if unknown != 0 {
			t.Errorf("Targets(%q): unknown = %d", tt.command, unknown)
		}
	}

	t.Run("over the budget", func(t *testing.T) {
		old := maxGlobEntries
		maxGlobEntries = 1
		t.Cleanup(func() { maxGlobEntries = old })
		if got, unknown := Targets("mv a/* .claude/", dir); len(got) != 0 || unknown != 1 {
			t.Errorf("%v, unknown %d; want none, 1 unknown", got, unknown)
		}
	})
	t.Run("past the target cap", func(t *testing.T) {
		for i := range maxTargets + 10 {
			if err := os.WriteFile(filepath.Join(dir, "a", fmt.Sprint("f", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := Targets("cp a/* .claude/", dir); len(got) != maxTargets {
			t.Errorf("%d targets, want the cap %d", len(got), maxTargets)
		}
	})
}

// A directory given as Git Bash writes it keeps that form when a file's name
// is joined to it, or Windows reads it as a path with no drive.
func TestTargetsCopyIntoGitBashPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	word := filepath.ToSlash(dir)
	if runtime.GOOS == "windows" {
		word = "/" + strings.ToLower(word[:1]) + word[2:]
	}
	file := filepath.Join(dir, ".claude", "settings.json")
	for _, c := range []string{
		"cp settings.json '" + word + "/.claude/'",
		"cp -t '" + word + "/.claude' settings.json",
	} {
		got, unknown := Targets(c, dir)
		if diff := cmp.Diff([]Target{{Path: file, Kind: Copy}}, got); diff != "" {
			t.Errorf("Targets(%q) (-want +got):\n%s", c, diff)
		}
		if unknown != 0 {
			t.Errorf("Targets(%q): unknown = %d", c, unknown)
		}
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

// The budget is in entries, not directories: everything up to it expands.
func TestTargetsGlobBudgetBound(t *testing.T) {
	flat := t.TempDir()
	for i := range maxGlobEntries {
		if err := os.WriteFile(filepath.Join(flat, fmt.Sprint("f", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, unknown := Targets("rm *", flat); len(got) != maxTargets || unknown != 0 {
		t.Errorf("%d files: %d targets, unknown %d", maxGlobEntries, len(got), unknown)
	}
	if err := os.WriteFile(filepath.Join(flat, "one more"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, unknown := Targets("rm *", flat); len(got) != 0 || unknown != 1 {
		t.Errorf("%d files: %d targets, unknown %d", maxGlobEntries+1, len(got), unknown)
	}

	dir := t.TempDir()
	for i := range 300 {
		d := filepath.Join(dir, fmt.Sprint("d", i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := range 5 {
			if err := os.WriteFile(filepath.Join(d, fmt.Sprint("f", j, ".go")), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, unknown := Targets("sed -i s/a/b/ */*.go", dir); len(got) != maxTargets || unknown != 0 {
		t.Errorf("300 directories of 5 files: %d targets, unknown %d", len(got), unknown)
	}
}

// The budgets span a command: once one is spent no glob reads anything, however
// many follow. A directory with nothing in it spends steps, as does a path.
func TestTargetsGlobBudgetHolds(t *testing.T) {
	old, oldSteps := maxGlobEntries, maxGlobSteps
	maxGlobEntries = 10
	t.Cleanup(func() { maxGlobEntries, maxGlobSteps = old, oldSteps })
	dir := t.TempDir()
	for i := range 50 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint("f", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	empty := t.TempDir()
	for _, tt := range []struct{ name, dir, command string }{
		{"rm", dir, "rm" + strings.Repeat(" *", 20)},
		{"cp", dir, "cp" + strings.Repeat(" *", 20) + " ."},
		{"mv", dir, "mv" + strings.Repeat(" *", 20) + " ."},
		{"redirect", dir, strings.Repeat("echo > *; ", 20)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := globRun(tt.command, tt.dir)
			// One more than the budget: that is how a read finds it spent.
			if r.entries > maxGlobEntries+1 {
				t.Errorf("read %d entries, budget %d", r.entries, maxGlobEntries)
			}
			if r.unknown == 0 {
				t.Error("no glob reported over the budget")
			}
		})
	}

	maxGlobEntries = old
	nested := t.TempDir()
	for _, d := range []string{"d1", "d2"} {
		if err := os.Mkdir(filepath.Join(nested, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The path of the directory itself is walked too, a step a segment.
	depth := func(dir string) int {
		return strings.Count(dir[len(filepath.VolumeName(dir)):], string(filepath.Separator))
	}
	for _, tt := range []struct {
		name, dir, command string
		steps              int // beyond the directory's own path
	}{
		{"an empty directory", empty, "rm *", 1},
		{"no such directory", empty, "rm no/*", 2},
		{"fixed segments first", empty, "rm a/b/c/*", 4},
		{"each match its own", nested, "rm */a/*", 5},
		{"a fixed last segment", nested, "rm */a", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := globRun(tt.command, tt.dir).steps, depth(tt.dir)+tt.steps; got != want {
				t.Errorf("took %d steps, want %d", got, want)
			}
		})
	}

	for _, tt := range []struct{ name, dir, command string }{
		{"empty directories", empty, "rm" + strings.Repeat(" *", 20)},
		{"no such directories", empty, "rm" + strings.Repeat(" no/*", 20)},
		{"a long path", empty, "rm " + strings.Repeat("a/", 20) + "*"},
		{"a long path after a glob", nested, "rm */" + strings.Repeat("a/", 20) + "*"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			maxGlobSteps = depth(tt.dir) + 8
			t.Cleanup(func() { maxGlobSteps = oldSteps })
			r := globRun(tt.command, tt.dir)
			if r.unknown == 0 {
				t.Errorf("no glob reported over a budget of %d steps", maxGlobSteps)
			}
			if r.steps > maxGlobSteps+depth(tt.dir)+21 {
				t.Errorf("took %d steps, budget %d", r.steps, maxGlobSteps)
			}
		})
	}
}

// globRun returns what command's globs spent.
func globRun(command, dir string) *results {
	r := &results{seen: map[string]int{}}
	p := &parser{src: command, res: r, vars: map[string]string{}, dyn: map[string]bool{}, dir: dir, dirOK: true}
	p.list("")
	return r
}

// A path as long as a command can be costs no more than its length: quadratic,
// the first of these takes over a minute.
func TestTargetsLongPathsLinear(t *testing.T) {
	long := strings.Repeat(strings.Repeat("a", 50)+"/", maxCommand/51-1)
	for _, command := range []string{
		"rm " + strings.Repeat("a/", maxCommand/2-3) + "*",
		"rm " + long + "*",
		"rm */" + strings.Repeat("a/", maxCommand/2-4) + "*",
		"rm */" + long + "x",
	} {
		start := time.Now()
		Targets(command, t.TempDir())
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("a path of %d bytes took %v", len(command), d)
		}
	}

	// Copying the path at each of its segments would stay under that bound, on
	// the step budget, but not this one.
	dir := t.TempDir()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	Targets("rm "+long+"*", dir)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 32<<20 {
		t.Errorf("a path of %d bytes allocated %d MiB", len(long), got>>20)
	}
}

// Linear in the command: quadratic, this takes seconds at the size cap.
func TestTargetsArraysLinear(t *testing.T) {
	for _, command := range []string{
		strings.Repeat("a=() ", maxCommand/5),
		"declare " + strings.Repeat("a=() ", (maxCommand-8)/5),
	} {
		start := time.Now()
		if got, _ := Targets(command, cwd); len(got) != 0 {
			t.Errorf("%d targets", len(got))
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%d bytes of arrays took %v", len(command), d)
		}
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
		"a=(", "a[", "a[b]+=(1) x", "f() (", "function f (", "time -p", "time -p --", "cp a/* b/",
		"rm [!", "rm [!]", "rm [!]]x", "rm []", "rm []]*", "rm a/b/[!c]/d",
	} {
		f.Add(s)
	}
	// A glob reads the real disk, from ~ or /, and that is not the time this
	// measures: TestTargetsGlobBudgetHolds holds the budgets.
	old, oldSteps := maxGlobEntries, maxGlobSteps
	maxGlobEntries, maxGlobSteps = 16, 64
	f.Cleanup(func() { maxGlobEntries, maxGlobSteps = old, oldSteps })
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
