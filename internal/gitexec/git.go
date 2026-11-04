package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/toolpath"
)

// ErrGit is a git command that failed. The error holds git's own message,
// cleaned.
var ErrGit = errors.New("git failed")

// GitTimeout bounds one local git command, and NetTimeout one that talks to
// the remote, when the context has no deadline of its own and Cmd sets none.
var (
	GitTimeout = 5 * time.Minute
	NetTimeout = 30 * time.Minute
)

// RemoteFlags go after toolpath.GitFlags on every git command run here: the
// remote comes from stele.json, which a clone brings. Only https and ssh may
// be used, so git can never run a transport command (ext::) or reach a local
// path; credential helpers never prompt; submodules are never fetched or
// pushed.
var RemoteFlags = []string{
	"-c", "protocol.allow=never",
	"-c", "protocol.https.allow=always",
	"-c", "protocol.ssh.allow=always",
	"-c", "credential.interactive=never",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "push.recurseSubmodules=no",
}

// network are the git commands that talk to the remote.
var network = map[string]bool{"clone": true, "fetch": true, "push": true, "ls-remote": true}

// TimeoutFor is the time limit for git with args, given the limit net for a
// command that talks to the remote: the first word that is not an option, nor
// the value of a -c, is the command.
func TimeoutFor(args []string, net time.Duration) time.Duration {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if network[a] {
			return net
		}
		break
	}
	return GitTimeout
}

// userinfo is the user and password part of a URL.
var userinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// Redact hides the user and password of every URL in s: a url.insteadOf in
// the user's git config can put a token there, and git prints the URL when
// it fails.
func Redact(s string) string { return userinfo.ReplaceAllString(s, "${1}***@") }

// quiet is added to git's environment: no prompt on the terminal, from Git
// Credential Manager or from an askpass program. An empty GIT_ASKPASS makes
// git skip core.askPass and SSH_ASKPASS too. ssh's own prompts (a key's
// passphrase, an unknown host key) are left alone: they come from the user's
// own ssh setup.
var quiet = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GCM_INTERACTIVE=never",
	"GIT_ASKPASS=",
	"GIT_PROTOCOL_FROM_USER=0",
}

// dropped are variables that would point git at another repository, index
// or object store than the one stele means.
var dropped = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_COMMON_DIR",
	"GIT_PREFIX",
}

// Cmd runs git safely.
type Cmd struct {
	Bin string   // the absolute path of git
	Env []string // the environment to start from, or nil for this process's
	// AllowFile lets git use its file transport, for a bare repository on
	// disk. Only tests set it, in code: it is never a flag, an environment
	// variable or a stele.json value.
	AllowFile bool
	// NetTimeout bounds a command that talks to the remote, or NetTimeout
	// when zero.
	NetTimeout time.Duration
}

// New is git at bin for the project at root. No PATH entry inside the
// project reaches it. The environment is worked out here, once: PATH can be
// long, and one stele command runs git many times.
func New(bin, root string) Cmd {
	return Cmd{Bin: bin, Env: toolpath.Env(os.Environ(), root)}
}

// Args returns the arguments to run git with: toolpath.GitFlags and
// RemoteFlags first.
func (g Cmd) Args(args []string) []string {
	out := append(toolpath.GitFlags(), RemoteFlags...)
	if g.AllowFile {
		out = append(out, "-c", "protocol.file.allow=always")
	}
	return append(out, args...)
}

// Environ returns git's environment: g's, without the variables in dropped,
// with quiet and extra.
func (g Cmd) Environ(extra []string) []string {
	base := g.Env
	if base == nil {
		base = os.Environ()
	}
	var out []string
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !dropName(name) {
			out = append(out, kv)
		}
	}
	out = append(out, quiet...)
	return append(out, extra...)
}

func dropName(name string) bool {
	for _, d := range dropped {
		if strings.EqualFold(name, d) {
			return true
		}
	}
	return false
}

// MaxOutput caps what stele keeps of git's standard output and of its
// standard error. The rest is read and dropped, so a remote or a repository
// cannot make stele hold more.
const MaxOutput = 1 << 20

// Capped is a writer that keeps the first Max bytes written to it and drops
// the rest. The buffer is a field, not embedded: an embedded bytes.Buffer
// would bring ReadFrom, which io.Copy would use instead of Write.
type Capped struct {
	buf bytes.Buffer
	Max int
}

func (c *Capped) Write(p []byte) (int, error) {
	if room := c.Max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *Capped) String() string { return c.buf.String() }

// Run runs git in dir and returns its standard output, up to MaxOutput
// bytes. stdin is what git reads, or nothing: git never reads the terminal.
func (g Cmd) Run(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) (string, error) {
	return g.RunLimit(ctx, dir, extraEnv, stdin, MaxOutput, args...)
}

// RunLimit is Run keeping up to limit bytes of standard output.
func (g Cmd) RunLimit(ctx context.Context, dir string, extraEnv []string, stdin []byte, limit int, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		net := g.NetTimeout
		if net == 0 {
			net = NetTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, TimeoutFor(args, net))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, g.Bin, g.Args(args)...)
	cmd.Dir = dir
	cmd.Env = g.Environ(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, errOut := &Capped{Max: limit}, &Capped{Max: MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		msg := Message(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return out.String(), &gitError{cmd: firstWord(args), msg: msg, code: code}
	}
	return out.String(), nil
}

// gitError is a git command that failed. It is ErrGit.
type gitError struct {
	cmd  string // the git subcommand
	msg  string // git's message, cleaned
	code int    // git's exit code, or -1
}

func (e *gitError) Error() string        { return fmt.Sprintf("%v: git %s: %s", ErrGit, e.cmd, e.msg) }
func (e *gitError) Is(target error) bool { return target == ErrGit }

// ExitCode returns git's exit code from an error Run returned, or -1.
func ExitCode(err error) int {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.code
	}
	return -1
}

func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// Message cleans what git wrote on stderr for printing. Lines the remote
// sent that hold a link are dropped: stele never shows a link the remote
// chose. A refusal such as "remote: Permission denied" stays. Credentials in
// a URL are hidden (Redact).
func Message(stderr string) string {
	var keep []string
	for _, line := range strings.Split(CleanLines(stderr), "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "remote:") && strings.Contains(line, "://") {
			continue
		}
		keep = append(keep, Redact(line))
	}
	return strings.Join(keep, "\n")
}
