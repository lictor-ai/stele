package publish

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/lictor-ai/stele/internal/toolpath"
)

var (
	// ErrToolNotFound is git, gh, glab or the browser opener missing from PATH.
	ErrToolNotFound = errors.New("not found on PATH")
	// ErrToolRefused is a program found inside the project: a cloned
	// repository could have put it there.
	ErrToolRefused = toolpath.ErrRefused
)

// lookPath finds a program on PATH. Tests replace it.
var lookPath = exec.LookPath

// resolveTool finds name on PATH and returns its absolute path. A program
// inside the project root, or inside the git repository that holds it, is
// refused (toolpath.Vet), and so is one found through a relative PATH entry.
func resolveTool(name, root string) (string, error) {
	p, err := lookPath(name)
	if errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("%s: found through a relative PATH entry: %w", name, ErrToolRefused)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrToolNotFound)
	}
	return toolpath.Vet(name, p, root)
}
