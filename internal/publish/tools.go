package publish

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// ErrToolNotFound is git, gh, glab or the browser opener missing from PATH.
	ErrToolNotFound = errors.New("not found on PATH")
	// ErrToolRefused is a program found inside the project: a cloned
	// repository could have put it there.
	ErrToolRefused = errors.New("refused: it is inside the project")
)

// lookPath finds a program on PATH. Tests replace it.
var lookPath = exec.LookPath

// resolveTool finds name on PATH and returns its absolute path. A program
// inside the project root is refused, as D43 refuses plugins from inside the
// repository: a PATH entry such as ./bin, or one a tool like direnv adds,
// would otherwise run whatever a cloned repository put there.
func resolveTool(name, root string) (string, error) {
	p, err := lookPath(name)
	if errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("%s: found through a relative PATH entry: %w", name, ErrToolRefused)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrToolNotFound)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if insideAny(abs, root) {
		return "", fmt.Errorf("%s at %s: %w", name, abs, ErrToolRefused)
	}
	return abs, nil
}

// insideAny reports whether p, or what it links to, is inside root or what
// root links to.
func insideAny(p, root string) bool {
	roots := []string{root}
	if r, err := filepath.Abs(root); err == nil {
		roots = append(roots, r)
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, r)
	}
	paths := []string{p}
	if e, err := filepath.EvalSymlinks(p); err == nil {
		paths = append(paths, e)
	}
	for _, x := range paths {
		for _, r := range roots {
			if within(x, r) {
				return true
			}
		}
	}
	return false
}

// within reports whether p is root or below it. Windows paths compare
// without case.
func within(p, root string) bool {
	if runtime.GOOS == "windows" {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
