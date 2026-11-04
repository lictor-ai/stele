package publish

import "github.com/lictor-ai/stele/internal/gitexec"

var (
	// ErrToolNotFound is git, gh, glab or the browser opener missing from PATH.
	ErrToolNotFound = gitexec.ErrToolNotFound
	// ErrToolRefused is a program found inside the project: a cloned
	// repository could have put it there.
	ErrToolRefused = gitexec.ErrToolRefused
)

// resolveTool finds name on PATH and returns its absolute path, never one
// inside the project (gitexec.Tool).
func resolveTool(name, root string) (string, error) { return gitexec.Tool(name, root) }
