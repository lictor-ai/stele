package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// rootMarkers make a directory a project root; the nearest wins.
var rootMarkers = []string{"stele.md", "stele.json", ".git"}

// findRoot locates the project root from dir (architecture §6): the nearest
// directory holding stele.md, stele.json or .git. A subproject with its own
// stele.md uses its rules (ADR-0004), and a .git (a directory, or the file a
// worktree or submodule has) ends the search: rules never cross into another
// checkout (#43).
func findRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if root, ok := walkUp(dir, rootMarkers...); ok {
		return root, nil
	}
	return "", fmt.Errorf("no stele.md, stele.json or git repository in %s or above", dir)
}

// holdsGit reports whether dir is the top of a git checkout: .git is a
// directory in a repository, a file in a worktree or submodule.
func holdsGit(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// walkUp returns the first directory from dir upwards that holds any of
// names. Any entry counts, a dangling link too, as holdsGit counts it: the
// hook and stele check must agree on where a project ends.
func walkUp(dir string, names ...string) (string, bool) {
	for {
		for _, name := range names {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
