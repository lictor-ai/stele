package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// subject is the first line of every commit stele publish makes.
func subject(path string) string { return "stele: publish " + path }

// projectTrailer names the project a publish commit came from.
const projectTrailer = "Stele-Project: "

// hashBlob writes data to the bare clone at dir as a blob and returns its
// id. Nothing is converted: the bytes are the committed stele.md.
func hashBlob(ctx context.Context, g gitCmd, dir string, data []byte) (string, error) {
	out, err := bare(ctx, g, dir, nil, data, "hash-object", "-w", "--no-filters", "--stdin")
	return strings.TrimSpace(out), err
}

// buildCommit makes a commit on baseRef in which path holds blob and every
// other file of the base stays as it is. It uses git's plumbing on a
// temporary index, so no file is checked out and no hook runs. The author and
// committer are the developer's identity as the project sees it: the bare
// clone reads only the global config.
func buildCommit(ctx context.Context, g gitCmd, dir, baseRef, path, blob string, c committed) (string, error) {
	tmp, err := os.MkdirTemp("", "stele-publish-index")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME=" + c.Name,
		"GIT_AUTHOR_EMAIL=" + c.Email,
		"GIT_COMMITTER_NAME=" + c.Name,
		"GIT_COMMITTER_EMAIL=" + c.Email,
	}
	if _, err := bare(ctx, g, dir, env, nil, "read-tree", baseRef); err != nil {
		return "", err
	}
	if _, err := bare(ctx, g, dir, env, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
		return "", err
	}
	tree, err := bare(ctx, g, dir, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	trailers := projectTrailer + c.Project + "\nProject-Commit: " + c.Head
	commit, err := bare(ctx, g, dir, env, nil, "commit-tree", strings.TrimSpace(tree), "-p", baseRef, "-m", subject(path), "-m", trailers)
	return strings.TrimSpace(commit), err
}

// isOurs reports whether commit is one stele publish made for this path and
// project: its subject is exactly subject(path), alone in its paragraph, and
// its last paragraph holds this project's trailer. A person's commit, or
// another project's publish of the same path, is not.
func isOurs(ctx context.Context, g gitCmd, dir, commit, path, project string) (bool, error) {
	out, err := bare(ctx, g, dir, nil, nil, "cat-file", "commit", commit)
	if err != nil {
		return false, err
	}
	_, msg, ok := strings.Cut(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n")
	if !ok {
		return false, nil
	}
	paras := strings.Split(strings.TrimRight(msg, "\n"), "\n\n")
	if len(paras) < 2 || paras[0] != subject(path) {
		return false, nil
	}
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		if line == projectTrailer+project {
			return true, nil
		}
	}
	return false, nil
}
