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
)

// ErrGit is a git command that failed. The error holds git's own message,
// cleaned.
var ErrGit = errors.New("git failed")

// localRemote, when set, is the URL git uses instead of "repo": a bare
// repository on disk. Only tests set it, and it alone allows git's file
// transport. It is not a flag, an environment variable or a stele.json value.
var localRemote string

// gitTimeout bounds one git command that has no deadline of its own.
var gitTimeout = 5 * time.Minute

// safety is passed to every git command. Only https and ssh may be used, so
// "repo" can never run a transport command (ext::) or reach a local path;
// credential helpers never prompt; submodules are never fetched or pushed.
var safety = []string{
	"-c", "protocol.allow=never",
	"-c", "protocol.https.allow=always",
	"-c", "protocol.ssh.allow=always",
	"-c", "credential.interactive=never",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "push.recurseSubmodules=no",
	"-c", "core.fsmonitor=false",
	// A bare repository is used only when named with --git-dir: a cloned
	// project laid out as one could otherwise bring its own config, with
	// filters and hooks that run commands.
	"-c", "safe.bareRepository=explicit",
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
	bin string // the absolute path of git
}

// args returns the arguments to run git with: safety first.
func (g gitCmd) args(args []string) []string {
	out := append([]string{}, safety...)
	if localRemote != "" {
		out = append(out, "-c", "protocol.file.allow=always")
	}
	return append(out, args...)
}

// env returns git's environment: this process's, without the variables in
// dropped, with quiet and extra.
func gitEnv(extra []string) []string {
	var out []string
	for _, kv := range os.Environ() {
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

// run runs git in dir and returns its standard output. stdin is what git
// reads, or nothing: git never reads the terminal.
func (g gitCmd) run(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gitTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, g.bin, g.args(args)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
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
	for _, line := range strings.Split(Clean(stderr), "\n") {
		line = strings.TrimRight(line, " \t")
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
