package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rootMarkers make a directory a project root; the nearest wins.
var rootMarkers = []string{"stele.md", "stele.json", ".git"}

// findRoot locates the project root from dir (architecture §6): the nearest
// directory holding stele.md, stele.json or a .git git would recognise. A
// subproject with its own stele.md uses its rules (ADR-0004), and a .git (a
// directory, or the file a worktree or submodule has) ends the search: rules
// never cross into another checkout (#43).
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

// holdsGit reports whether dir is the top of a git checkout: its .git is a
// directory git would take for a repository, or, in a worktree or submodule,
// a file whose "gitdir: <path>" leads to one. A .git git would not recognise
// is any other folder or file: git lists the files beside it as its own
// repository's, so it must not end a project here either. Only files are
// read, no git is run: the hook calls this.
func holdsGit(dir string) bool {
	dotgit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotgit)
	switch {
	case err != nil:
		return false
	case info.IsDir():
		return isGitDir(dotgit)
	case !info.Mode().IsRegular():
		return false
	}
	target, ok := readGitlink(dotgit)
	if !ok {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return isGitDir(target)
}

// maxGitFile caps what holdsGit reads of a .git file or a commondir file:
// both hold one path.
const maxGitFile = 4096

// readGitlink returns the path in a .git file, "gitdir: <path>", as git
// reads it: the whole file, trailing white space trimmed.
func readGitlink(file string) (string, bool) {
	data, ok := readSmall(file)
	if !ok {
		return "", false
	}
	rest, ok := bytes.CutPrefix(data, []byte("gitdir: "))
	if !ok {
		return "", false
	}
	p := strings.TrimRight(string(rest), " \t\r\n")
	if p == "" || strings.ContainsAny(p, "\n\x00") {
		return "", false
	}
	return filepath.FromSlash(p), true
}

// isGitDir reports whether d is a directory git would take for a
// repository: a HEAD file, and objects/ and refs/ in it, or in the common
// directory its commondir file names, as a worktree's has.
func isGitDir(d string) bool {
	if info, err := os.Stat(filepath.Join(d, "HEAD")); err != nil || !info.Mode().IsRegular() {
		return false
	}
	common := d
	if data, ok := readSmall(filepath.Join(d, "commondir")); ok {
		p := filepath.FromSlash(strings.TrimRight(string(data), " \t\r\n"))
		if p == "" {
			return false
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(d, p)
		}
		common = p
	}
	for _, sub := range []string{"objects", "refs"} {
		if info, err := os.Stat(filepath.Join(common, sub)); err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// readSmall reads a regular file of at most maxGitFile bytes. It looks
// before it opens: opening a fifo would block the hook.
func readSmall(file string) ([]byte, bool) {
	if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() || info.Size() > maxGitFile {
		return nil, false
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGitFile+1))
	if err != nil || len(data) > maxGitFile {
		return nil, false
	}
	return data, true
}

// walkUp returns the first directory from dir upwards that holds any of
// names. Any entry counts, a dangling link too, except .git, which counts
// only as holdsGit counts it: the hook and stele check must agree on where a
// project ends.
func walkUp(dir string, names ...string) (string, bool) {
	for {
		for _, name := range names {
			if name == ".git" {
				if holdsGit(dir) {
					return dir, true
				}
				continue
			}
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
