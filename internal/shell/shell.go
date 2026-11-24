// Package shell finds the files a shell command writes, as far as reading
// the command can tell. It never runs the command. stele uses it before a
// Bash call, so a file written through Bash is not invisible (#57): only the
// Write and Edit tools show the agent a file's rules and check what it wrote.
//
// It understands the forms an agent uses to write a file: redirections
// (heredoc bodies skipped), tee, sed -i and perl -i, plus rm, mv, cp, ln,
// truncate, touch, mkdir, and the .git folders git init, clone, worktree add
// and submodule add make, for stele's own files and a .git below a project.
// It follows cd, simple NAME=value variables and globs, and reads the literal
// string of bash -c, sh -c, zsh -c, dash -c, ksh -c and eval as commands: one
// it cannot read whole counts as unknown.
// Scripts, generators, xargs, find -exec and targets it cannot
// resolve are not seen: Targets counts the last kind as unknown. A command it
// cannot parse is read up to the error: nothing after it runs when bash agrees,
// and it hides only what follows a form stele does not know when bash does not.
// (( is always read as arithmetic up to its )): what subshells written that way
// write is not seen, and with no )) nothing after it is. shopt options are not
// followed.
package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind is how a command touches a file.
type Kind int

const (
	Redirect    Kind = iota // > >> >| <> &> &>> n> >&file
	Tee                     // an operand of tee
	SedInPlace              // a file of sed -i
	PerlInPlace             // a file of perl -i
	Remove                  // an operand of rm, a source of mv
	Copy                    // what cp, mv, ln or truncate leave there
	Create                  // a file or folder touch, mkdir or git makes
)

// Writes reports whether the kind writes content, which rules are about.
// Remove, Copy and Create matter only for stele's own files.
func (k Kind) Writes() bool { return k <= PerlInPlace }

// Target is a file a command writes.
type Target struct {
	Path string // absolute, cleaned
	Kind Kind
}

// The caps bound what one command can cost.
const (
	maxCommand = 256 << 10  // longer: nothing is read, one unknown
	maxDepth   = 16         // subshells and substitutions
	maxTargets = 64         // after duplicates are merged
	maxGlob    = 1000       // files a command's globs may match, in all
	maxNested  = maxCommand // bytes of nested scripts read, in all: eval, bash -c, iex
	maxExpand  = 1 << 20    // bytes variables expand to, in all
)

// windows is whether paths follow Windows rules; getenv reads the
// environment. Tests replace them.
var (
	windows = runtime.GOOS == "windows"
	getenv  = os.Getenv
)

// Targets returns the files command writes, resolved from cwd, in the order
// they appear, each once. unknown counts targets it saw but could not
// resolve: a variable it does not know, a substitution, a path with no
// directory to resolve it against.
func Targets(command, cwd string) (ts []Target, unknown int) {
	if len(command) > maxCommand {
		return nil, 1
	}
	r := &results{seen: map[string]int{}}
	p := &parser{src: command, res: r, vars: map[string]string{}, dyn: map[string]bool{}, dir: cwd, dirOK: cwd != ""}
	p.list("")
	return r.out, r.unknown
}

// results is what every parser of one command adds to.
type results struct {
	out     []Target
	seen    map[string]int // path to its index in out
	unknown int
	globbed int // files matched by globs
	entries int // directory entries globs read
	steps   int // directories globs opened and fixed path segments they walked
	nested  int // bytes of nested scripts read, each read counted readCost more
	grown   int // bytes variables expanded to; maxExpand+1 once spent
	arith   int // bytes arithmetic was scanned for assignments, a cost tests bound
	envs    map[string]string
}

// env reads an environment variable once a command: on Windows each read
// copies the value, so a variable named many times would cost that many
// copies, past the budget too.
func (r *results) env(name string) string {
	if v, ok := r.envs[name]; ok {
		return v
	}
	if r.envs == nil {
		r.envs = map[string]string{}
	}
	v := getenv(name)
	r.envs[name] = v
	return v
}

// readCost is what reading a nested script costs beyond its bytes: a parser
// set up, its words and statements. It keeps many tiny reads, as of a
// variable that holds itself, to about a thousand.
const readCost = 256

// reread counts a nested script of n bytes about to be read: a variable
// expanded into eval or iex can hold itself, so the depth alone does not
// bound the work. It reports whether the script is within the budget.
func (r *results) reread(n int) bool {
	if r.nested > maxNested {
		return false
	}
	r.nested += n + readCost
	return r.nested <= maxNested
}

// expand counts n bytes of a variable's value about to be written into a
// word: a command under maxCommand can name a long variable many times, or
// double it again and again, and expand to gigabytes (#171). It reports
// whether they are within the budget. The first expansion past it counts as
// one unknown; it and every expansion after it are not written, so their
// words are unknown, and the count stops one past the budget. The budget is
// four times the longest command: paths and short values, expanded as often
// as a command written by hand or an agent does, stay far below it, and a
// value as long as the command still expands four times.
func (r *results) expand(n int) bool {
	if r.grown > maxExpand {
		return false
	}
	if n > maxExpand-r.grown {
		r.grown = maxExpand + 1 // spent
		r.unknown++
		return false
	}
	r.grown += n
	return true
}

// add adds one target, merging a duplicate. A content write wins over a
// removal or a copy of the same file.
func (r *results) add(path string, k Kind) {
	if i, ok := r.seen[path]; ok {
		if k.Writes() && !r.out[i].Kind.Writes() {
			r.out[i].Kind = k
		}
		return
	}
	if len(r.out) >= maxTargets {
		return
	}
	r.seen[path] = len(r.out)
	r.out = append(r.out, Target{Path: path, Kind: k})
}

// Abs resolves word, a path as a command wrote it, against dir. On Windows,
// Git Bash's /c/... is C:\..., C:\... and C:/... are absolute, and any other
// leading slash or a drive-relative C:x is unknown: it is not under dir. A
// relative word with no dir is unknown.
func Abs(dir, word string) (string, bool) {
	if word == "" {
		return "", false
	}
	if windows {
		switch {
		case len(word) >= 2 && word[0] == '/' && letter(word[1]) && (len(word) == 2 || word[2] == '/'):
			return filepath.Clean(strings.ToUpper(word[1:2]) + `:\` + word[min(len(word), 3):]), true
		case len(word) >= 3 && letter(word[0]) && word[1] == ':' && (word[2] == '\\' || word[2] == '/'):
			return filepath.Clean(word), true
		case word[0] == '/' || word[0] == '\\', len(word) >= 2 && word[1] == ':':
			return "", false
		}
	} else if word[0] == '/' {
		return filepath.Clean(word), true
	}
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, word), true
}

func letter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// device reports whether word names a device, not a file.
func device(word string) bool {
	if strings.HasPrefix(word, "/dev/") || strings.HasPrefix(word, "/proc/self/fd/") {
		return true
	}
	if !windows {
		return false
	}
	base := strings.ToUpper(filepath.Base(word))
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "NUL", "CON", "PRN", "AUX":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && '1' <= base[3] && base[3] <= '9'
}
