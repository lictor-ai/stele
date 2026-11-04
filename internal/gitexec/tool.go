package gitexec

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/lictor-ai/stele/internal/toolpath"
)

var (
	// ErrToolNotFound is a program missing from PATH.
	ErrToolNotFound = errors.New("not found on PATH")
	// ErrToolRefused is a program found inside the project: a cloned
	// repository could have put it there.
	ErrToolRefused = toolpath.ErrRefused
)

// LookPath finds a program on PATH. Tests replace it.
var LookPath = exec.LookPath

// Tool finds name on PATH and returns its absolute path. A program inside
// the project root, or inside the git repository that holds it, is refused
// (toolpath.Vet), and so is one found through a relative PATH entry.
func Tool(name, root string) (string, error) {
	p, err := LookPath(name)
	if errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("%s: found through a relative PATH entry: %w", name, ErrToolRefused)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrToolNotFound)
	}
	return toolpath.Vet(name, p, root)
}
