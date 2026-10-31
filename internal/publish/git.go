package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/toolpath"
)

// ErrGit is a git command that failed. The error holds git's own message,
// cleaned.
var ErrGit = errors.New("git failed")

// localRemote, when set, is the URL git uses instead of "repo": a bare
// repository on disk. Only tests set it, and it alone allows git's file
// transport. It is not a flag, an environment variable or a stele.json value.
var localRemote string

// gitTimeout bounds one local git command, and netTimeout one that talks to
// the remote, when the context has no deadline of its own.
var (
	gitTimeout = 5 * time.Minute
	netTimeout = 30 * time.Minute
)

// remoteFlags go after toolpath.GitFlags on every git command, and are stele
// publish's alone: "repo" comes from stele.json, which a clone brings. Only
// https and ssh may be used, so it can never run a transport command (ext::)
// or reach a local path; credential helpers never prompt; submodules are
// never fetched or pushed.
var remoteFlags = []string{
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

// timeoutFor is the time limit for git with args: the first word that is not
// an option is the command.
func timeoutFor(args []string) time.Duration {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if network[a] {
			return netTimeout
		}
		break
	}
	return gitTimeout
}

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

// gitCmd runs git safely.
type gitCmd struct {
	bin string   // the absolute path of git
	env []string // the environment to start from, or nil for this process's
}

// newGitCmd is git at bin for the project at root. No PATH entry inside the
// project reaches it. The environment is worked out here, once: PATH can be
// long, and one publish runs git many times.
func newGitCmd(bin, root string) gitCmd {
	return gitCmd{bin: bin, env: toolpath.Env(os.Environ(), root)}
}

// args returns the arguments to run git with: toolpath.GitFlags and
// remoteFlags first.
func (g gitCmd) args(args []string) []string {
	out := append(toolpath.GitFlags(), remoteFlags...)
	if localRemote != "" {
		out = append(out, "-c", "protocol.file.allow=always")
	}
	return append(out, args...)
}

// environ returns git's environment: g's, without the variables in dropped,
// with quiet and extra.
func (g gitCmd) environ(extra []string) []string {
	base := g.env
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

// maxOutput caps what stele keeps of git's standard output and of its
// standard error. The rest is read and dropped, so a remote or a repository
// cannot make stele hold more.
const maxOutput = 1 << 20

// capped is a writer that keeps the first max bytes written to it and drops
// the rest. The buffer is a field, not embedded: an embedded bytes.Buffer
// would bring ReadFrom, which io.Copy would use instead of Write.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// run runs git in dir and returns its standard output, up to maxOutput
// bytes. stdin is what git reads, or nothing: git never reads the terminal.
func (g gitCmd) run(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) (string, error) {
	return g.runLimit(ctx, dir, extraEnv, stdin, maxOutput, args...)
}

// runLimit is run keeping up to limit bytes of standard output.
func (g gitCmd) runLimit(ctx context.Context, dir string, extraEnv []string, stdin []byte, limit int, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeoutFor(args))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, g.bin, g.args(args)...)
	cmd.Dir = dir
	cmd.Env = g.environ(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, errOut := &capped{max: limit}, &capped{max: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		msg := gitMessage(errOut.String())
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

// exitCode returns git's exit code from an error run returned, or -1.
func exitCode(err error) int {
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

// gitMessage cleans what git wrote on stderr for printing. Lines the remote
// sent that hold a link are dropped: stele never shows a link the remote
// chose. A refusal such as "remote: Permission denied" stays.
func gitMessage(stderr string) string {
	var keep []string
	for _, line := range strings.Split(CleanLines(stderr), "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "remote:") && strings.Contains(line, "://") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
