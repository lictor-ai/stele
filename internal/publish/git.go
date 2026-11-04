package publish

import "github.com/lictor-ai/stele/internal/gitexec"

// ErrGit is a git command that failed. The error holds git's own message,
// cleaned.
var ErrGit = gitexec.ErrGit

// localRemote, when set, is the URL git uses instead of "repo": a bare
// repository on disk. Only tests set it, and it alone allows git's file
// transport. It is not a flag, an environment variable or a stele.json value.
var localRemote string

// gitCmd runs git safely: see internal/gitexec.
type gitCmd = gitexec.Cmd

// maxOutput caps what stele keeps of a program's standard output.
const maxOutput = gitexec.MaxOutput

// newGitCmd is git at bin for the project at root. No PATH entry inside the
// project reaches it.
func newGitCmd(bin, root string) gitCmd {
	g := gitexec.New(bin, root)
	g.AllowFile = localRemote != ""
	return g
}

// exitCode returns git's exit code from an error Run returned, or -1.
func exitCode(err error) int { return gitexec.ExitCode(err) }
