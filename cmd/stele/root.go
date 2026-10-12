package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// findRoot locates the project root from dir (architecture §6): the nearest
// directory holding stele.md or stele.json, else the nearest holding .git.
// stele.md comes first so a subproject with its own rules uses them.
func findRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if root, ok := walkUp(dir, "stele.md", "stele.json"); ok {
		return root, nil
	}
	if root, ok := walkUp(dir, ".git"); ok {
		return root, nil
	}
	return "", fmt.Errorf("no stele.md, stele.json or git repository in %s or above", dir)
}

// walkUp returns the first directory from dir upwards that holds any of names.
func walkUp(dir string, names ...string) (string, bool) {
	for {
		for _, name := range names {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
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
